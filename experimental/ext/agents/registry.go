package agents

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// Config is the input to Register.
type Config struct {
	// Server is the mcpkit server to wire the agents/list and agents/get
	// handlers onto. Required. Agent tool names resolve against its tool
	// registry.
	Server *server.Server

	// Agents is the initial roster the server advertises. Name must be
	// unique; Register drops later duplicates with a returned error. May be
	// empty and populated at runtime via Registry.AddAgent.
	Agents []AgentDef

	// PageSize caps the agents per agents/list page. Zero, the default,
	// returns the whole roster with no nextCursor, matching mcpkit's
	// server-wide default for the core list methods.
	PageSize int

	// TTLMs is the SEP-2549 freshness hint stamped on both results. Nil omits
	// it.
	TTLMs *int

	// CacheScope is the SEP-2549 cache scope stamped on both results. Empty
	// means core.CacheScopePrivate, which the SEP asks for unless every
	// caller gets an identical response. Set core.CacheScopePublic only when
	// Visible is nil and no agent's content varies by caller.
	CacheScope string

	// Visible decides whether the calling client may see an agent. Nil shows
	// every agent to everyone. An agent it rejects is absent from agents/list
	// and answers agents/get exactly as a nonexistent name does, so callers
	// cannot probe for hidden agents.
	Visible func(ctx core.MethodContext, name string) bool

	// TracerProvider opts the discovery handlers into SEP-414 spans
	// (agents.list / agents.get). Nil or core.NoopTracerProvider{}, the
	// default, skips emission with zero allocation, the same opt-in contract
	// ext/tasks and events use. The extension depends only on the core
	// tracing abstraction, never on ext/otel.
	TracerProvider core.TracerProvider
}

// Registry is the runtime handle Register returns. It owns the agent map the
// two method handlers consult on every request, and exposes AddAgent /
// RemoveAgent so authors can reconfigure the roster after Register has run.
//
// Ordering: agents/list returns agents in insertion order (the order they were
// passed to Register, then the order they were AddAgent'd), so the wire roster
// and its cursors are stable rather than map-iteration-random.
//
// Concurrency: AddAgent / RemoveAgent take an internal write lock; per-request
// lookups take a read lock. An agent added during a concurrent list is either
// fully visible or not at all. Cursors are offsets, so a roster change between
// two page requests can shift entries across the page boundary.
type Registry struct {
	mu     sync.RWMutex
	byName map[string]AgentDef
	order  []string
	srv    *server.Server
	cfg    Config
	// tp is the discovery TracerProvider, defaulted to core.NoopTracerProvider{}
	// by Register so the handlers can StartSpan unconditionally.
	tp core.TracerProvider
}

// Register declares the agents extension on cfg.Server and wires the
// agents/list and agents/get handlers, returning a Registry for runtime
// roster changes. It is the single entry point for the server side of the
// primitive, mirroring events.Register.
//
// The returned error is advisory: the handlers are wired and the Registry is
// usable either way, so a typo in one agent does not take the whole server
// down. It reports duplicate names (the first wins, later ones are dropped)
// and agents naming tools the server has not registered (the agent is kept,
// and agents/get answers invalid_agent_definition for it until the tool
// exists). Register tools before agents for the second check to be useful.
func Register(cfg Config) (*Registry, error) {
	tp := cfg.TracerProvider
	if tp == nil {
		tp = core.NoopTracerProvider{}
	}
	if cfg.CacheScope == "" {
		cfg.CacheScope = core.CacheScopePrivate
	}
	r := &Registry{
		byName: make(map[string]AgentDef),
		srv:    cfg.Server,
		cfg:    cfg,
		tp:     tp,
	}

	var dupes []string
	var errs []error
	for _, def := range cfg.Agents {
		if _, exists := r.byName[def.Name]; exists {
			dupes = append(dupes, def.Name)
			continue
		}
		r.byName[def.Name] = def
		r.order = append(r.order, def.Name)
		if err := r.checkTools(def); err != nil {
			errs = append(errs, err)
		}
	}
	if len(dupes) > 0 {
		errs = append([]error{fmt.Errorf("agents: dropped duplicate name(s): %v", dupes)}, errs...)
	}

	cfg.Server.RegisterExtension(AgentsExtension{})
	cfg.Server.HandleMethod(MethodList, r.handleList)
	cfg.Server.HandleMethod(MethodGet, r.handleGet)

	return r, errors.Join(errs...)
}

// AddAgent adds an agent to the roster at runtime. A duplicate name is
// rejected and the existing agent is left untouched. An agent naming an
// unregistered tool is added anyway and reported in the returned error, the
// same advisory contract as Register. New agents appear at the end of the
// agents/list order.
func (r *Registry) AddAgent(def AgentDef) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byName[def.Name]; exists {
		return fmt.Errorf("agents: agent %q already registered", def.Name)
	}
	r.byName[def.Name] = def
	r.order = append(r.order, def.Name)
	return r.checkTools(def)
}

// RemoveAgent drops an agent from the roster. Removing an unknown name is a
// no-op, so a caller reconciling a desired roster need not guard every
// delete.
func (r *Registry) RemoveAgent(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byName[name]; !exists {
		return
	}
	delete(r.byName, name)
	for i, n := range r.order {
		if n == name {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
}

// checkTools reports the tool names in def that the server catalog does not
// hold.
func (r *Registry) checkTools(def AgentDef) error {
	var missing []string
	for _, name := range def.Tools {
		if _, ok := r.srv.Registry().ToolDef(name); !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("agents: agent %q names unregistered tool(s): %v", def.Name, missing)
	}
	return nil
}

func (r *Registry) visible(ctx core.MethodContext, name string) bool {
	return r.cfg.Visible == nil || r.cfg.Visible(ctx, name)
}

func (r *Registry) handleList(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
	_, span := r.tp.StartSpan(ctx, "agents.list")
	defer span.End()

	var p ListAgentsParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "agents/list: invalid params: "+err.Error())
		}
	}

	r.mu.RLock()
	names := append([]string(nil), r.order...)
	defs := make([]AgentDef, len(names))
	for i, n := range names {
		defs[i] = r.byName[n]
	}
	r.mu.RUnlock()

	cards := make([]AgentCard, 0, len(defs))
	for _, def := range defs {
		if r.visible(ctx, def.Name) {
			cards = append(cards, def.Card())
		}
	}

	page, next, err := paginate(cards, p.Cursor, r.cfg.PageSize)
	if err != nil {
		return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "agents/list: invalid cursor")
	}

	span.SetAttribute("agents.count", strconv.Itoa(len(page)))
	return core.NewResponse(id, ListAgentsResult{
		ResultType: core.ResultTypeComplete,
		Agents:     page,
		NextCursor: next,
		TTLMs:      r.cfg.TTLMs,
		CacheScope: r.cfg.CacheScope,
	})
}

func (r *Registry) handleGet(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
	_, span := r.tp.StartSpan(ctx, "agents.get")
	defer span.End()
	span.SetAttribute("agents.found", "false")

	var p GetAgentParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "agents/get: invalid params: "+err.Error())
		}
	}
	span.SetAttribute("mcp.agent.id", p.Name)
	if p.Name == "" {
		return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "agents/get: name is required")
	}

	r.mu.RLock()
	def, ok := r.byName[p.Name]
	r.mu.RUnlock()
	if !ok || !r.visible(ctx, p.Name) {
		return core.NewErrorResponseWithData(id, core.ErrCodeInvalidParams, "agents/get: agent not found",
			ErrorData{Reason: ReasonAgentNotFound})
	}
	span.SetAttribute("agents.found", "true")

	tools, missing := r.resolveTools(ctx, def)
	if len(missing) > 0 {
		return core.NewErrorResponseWithData(id, core.ErrCodeInternal,
			fmt.Sprintf("agents/get: agent %q names unregistered tool(s): %v", def.Name, missing),
			ErrorData{Reason: ReasonInvalidAgentDefinition})
	}

	return core.NewResponse(id, GetAgentResult{
		ResultType:       core.ResultTypeComplete,
		Agent:            def.Name,
		Instructions:     def.Instructions,
		ModelRoutingHint: def.ModelRoutingHint,
		Tools:            tools,
		TTLMs:            r.cfg.TTLMs,
		CacheScope:       r.cfg.CacheScope,
	})
}

// resolveTools looks each of def's tool names up in the server registry and
// applies the per-client rewrite tools/list applies, so every returned
// definition matches the caller's tools/list entry field for field. Today
// that rewrite is the SEP-2356 x-mcp-file strip for clients that did not
// declare fileInputs.
func (r *Registry) resolveTools(ctx core.MethodContext, def AgentDef) ([]core.ToolDef, []string) {
	caps := ctx.ClientCaps()
	strip := caps == nil || caps.FileInputs == nil

	tools := make([]core.ToolDef, 0, len(def.Tools))
	var missing []string
	for _, name := range def.Tools {
		td, ok := r.srv.Registry().ToolDef(name)
		if !ok {
			missing = append(missing, name)
			continue
		}
		if strip {
			td.InputSchema = core.StripFileInputKeywords(td.InputSchema)
		}
		tools = append(tools, td)
	}
	return tools, missing
}

// paginate slices items by an opaque base64 offset cursor. A zero page size
// returns everything with no cursor. A cursor that does not decode, or points
// past the end, is an error the caller maps to -32602.
func paginate[T any](items []T, cursor string, pageSize int) ([]T, string, error) {
	start := 0
	if cursor != "" {
		raw, err := base64.StdEncoding.DecodeString(cursor)
		if err != nil {
			return nil, "", err
		}
		start, err = strconv.Atoi(string(raw))
		if err != nil || start < 0 || start > len(items) {
			return nil, "", fmt.Errorf("cursor out of range")
		}
	}
	if pageSize <= 0 {
		return items[start:], "", nil
	}
	end := start + pageSize
	if end >= len(items) {
		return items[start:], "", nil
	}
	return items[start:end], base64.StdEncoding.EncodeToString([]byte(strconv.Itoa(end))), nil
}
