package agents

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// Config is the input to Register.
type Config struct {
	// Server is the mcpkit server to wire the agents/list and agents/get
	// handlers onto. Required.
	Server *server.Server

	// Agents is the initial roster the server advertises. AgentID must be
	// unique; Register drops later duplicates with a returned error. May be
	// empty and populated at runtime via Registry.AddAgent.
	Agents []AgentDef
}

// Registry is the runtime handle Register returns. It owns the thread-safe
// agent map the two method handlers consult on every request, and exposes
// AddAgent / RemoveAgent so authors can reconfigure the roster after Register
// has run.
//
// Ordering: agents/list returns agents in insertion order (the order they were
// passed to Register, then the order they were AddAgent'd), so the wire roster
// is stable and deterministic rather than map-iteration-random.
//
// Concurrency: AddAgent / RemoveAgent take an internal write lock; per-request
// lookups inside the handlers take a read lock. An agent added during a
// concurrent list is either fully visible or not at all.
type Registry struct {
	mu    sync.RWMutex
	byID  map[string]AgentDef
	order []string
	srv   *server.Server
}

// Register declares the agents extension on cfg.Server and wires the
// agents/list and agents/get handlers, returning a Registry for runtime
// roster changes. It is the single entry point for the server side of the
// primitive, mirroring events.Register.
//
// A duplicate AgentID in cfg.Agents is an author bug: the first wins, later
// duplicates are dropped, and the returned error names them. The handlers are
// still wired and the Registry is usable — the error is advisory so a typo in
// one agent does not take the whole server down.
func Register(cfg Config) (*Registry, error) {
	r := &Registry{
		byID: make(map[string]AgentDef),
		srv:  cfg.Server,
	}

	var dupes []string
	for _, def := range cfg.Agents {
		if _, exists := r.byID[def.AgentID]; exists {
			dupes = append(dupes, def.AgentID)
			continue
		}
		r.byID[def.AgentID] = def
		r.order = append(r.order, def.AgentID)
	}

	cfg.Server.RegisterExtension(AgentsExtension{})
	cfg.Server.HandleMethod(MethodList, r.handleList)
	cfg.Server.HandleMethod(MethodGet, r.handleGet)

	if len(dupes) > 0 {
		return r, fmt.Errorf("agents: dropped duplicate agentId(s): %v", dupes)
	}
	return r, nil
}

// AddAgent adds an agent to the roster at runtime. Returns an error if an
// agent with the same AgentID is already registered (the existing one is left
// untouched). New agents appear at the end of the agents/list order.
func (r *Registry) AddAgent(def AgentDef) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byID[def.AgentID]; exists {
		return fmt.Errorf("agents: agent %q already registered", def.AgentID)
	}
	r.byID[def.AgentID] = def
	r.order = append(r.order, def.AgentID)
	return nil
}

// RemoveAgent drops an agent from the roster. Removing an unknown AgentID is a
// no-op (app state, not an error) — a caller reconciling a desired roster
// should not have to guard every delete.
func (r *Registry) RemoveAgent(agentID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byID[agentID]; !exists {
		return
	}
	delete(r.byID, agentID)
	for i, id := range r.order {
		if id == agentID {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
}

// handleList answers agents/list with the roster of summaries (no tool
// schemas). Params are ignored — the roster is unfiltered today.
func (r *Registry) handleList(ctx core.MethodContext, id json.RawMessage, _ json.RawMessage) *core.Response {
	r.mu.RLock()
	summaries := make([]AgentSummary, 0, len(r.order))
	for _, agentID := range r.order {
		summaries = append(summaries, r.byID[agentID].Summary())
	}
	r.mu.RUnlock()
	return core.NewResponse(id, ListResult{Agents: summaries})
}

// handleGet answers agents/get. An empty or unknown agentId is a client error:
// empty -> InvalidParams, unknown -> InvalidParams with a "not found" message.
// A known agentId returns the full AgentDetail (summary + instructions +
// scoped tools).
func (r *Registry) handleGet(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
	var p GetParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "agents/get: invalid params: "+err.Error())
		}
	}
	if p.AgentID == "" {
		return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "agents/get: agentId is required")
	}

	r.mu.RLock()
	def, ok := r.byID[p.AgentID]
	r.mu.RUnlock()
	if !ok {
		return core.NewErrorResponse(id, core.ErrCodeInvalidParams, fmt.Sprintf("agents/get: unknown agentId %q", p.AgentID))
	}
	return core.NewResponse(id, GetResult{Agent: def.Detail()})
}
