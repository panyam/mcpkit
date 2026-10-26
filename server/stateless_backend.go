package server

import (
	"context"

	core "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server/stateless"
)

// statelessBackend adapts a *Server (with its embedded Dispatcher and
// Registry) to the stateless.Backend interface. Lives in package server
// rather than server/stateless so it can read registry private fields
// without exporting more API surface.
//
// One instance per Server, constructed by newStreamableTransport when
// statelessMode != stateless.ModeLegacyOnly.
type statelessBackend struct {
	s *Server
}

// newStatelessBackend builds a Backend bound to the given server.
func newStatelessBackend(s *Server) *statelessBackend {
	return &statelessBackend{s: s}
}

// Compile-time check: the adapter satisfies the stateless package's
// interface. If the interface ever gains a method, this line breaks
// first — loudest signal we can give.
var _ stateless.Backend = (*statelessBackend)(nil)

// reg is a small accessor to keep the read sites tidy. The Registry
// lives on the legacy session Dispatcher today; we read through it
// for now. When the legacy dispatcher is retired, this accessor moves
// to whatever owns the shared registry post-cleanup.
func (b *statelessBackend) reg() *Registry { return b.s.dispatcher.Reg }

// ServerInfo returns the server identity stamped into every stateless
// result's _meta. Sourced from NewServer's first argument.
func (b *statelessBackend) ServerInfo() core.Implementation {
	return b.s.dispatcher.serverInfo
}

// Instructions returns the WithInstructions text for server/discover.
func (b *statelessBackend) Instructions() string {
	return b.s.dispatcher.instructions
}

// Capabilities returns the capability shape derived from registered
// handlers + active extensions. Mirrors the legacy initialize handshake's
// computation modulo session-only fields.
func (b *statelessBackend) Capabilities() core.ServerCapabilities {
	caps := core.ServerCapabilities{}

	r := b.reg()
	r.mu.RLock()
	hasTools := len(r.tools) > 0
	hasResources := len(r.resources) > 0 || len(r.templates) > 0
	hasPrompts := len(r.prompts) > 0
	hasCompletions := len(r.completions) > 0
	r.mu.RUnlock()

	if hasTools {
		caps.Tools = &core.ToolsCap{ListChanged: true}
	}
	if hasResources {
		caps.Resources = &core.ResourcesCap{ListChanged: true}
	}
	if hasPrompts {
		caps.Prompts = &core.PromptsCap{ListChanged: true}
	}
	if hasCompletions {
		caps.Completions = &struct{}{}
	}

	if exts := b.s.dispatcher.extensions; len(exts) > 0 {
		caps.Extensions = make(map[string]core.ExtensionCapability, len(exts))
		for id, e := range exts {
			caps.Extensions[id] = core.ExtensionCapability(e.Settings)
		}
		caps.TopLevel = extensionTopLevel(exts)
	}
	return caps
}

// SupportedVersions returns the stateless-wire version list this build
// speaks. Distinct from the legacy initialize negotiation list — the
// two surfaces are advertised independently.
func (b *statelessBackend) SupportedVersions() []string {
	return core.SupportedStatelessVersions
}

// Tools returns a snapshot of registered tools in registration order.
func (b *statelessBackend) Tools() []core.ToolDef {
	r := b.reg()
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]core.ToolDef, 0, len(r.toolOrder))
	for _, name := range r.toolOrder {
		if e, ok := r.tools[name]; ok {
			out = append(out, e.def)
		}
	}
	return out
}

// Tool returns def + handler for a registered tool.
func (b *statelessBackend) Tool(name string) (core.ToolDef, core.ToolHandler, bool) {
	r := b.reg()
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.tools[name]
	if !ok {
		return core.ToolDef{}, nil, false
	}
	return e.def, e.handler, true
}

// Resources returns a snapshot of concrete resources in registration order.
func (b *statelessBackend) Resources() []core.ResourceDef {
	r := b.reg()
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]core.ResourceDef, 0, len(r.resourceOrder))
	for _, uri := range r.resourceOrder {
		if e, ok := r.resources[uri]; ok {
			out = append(out, e.def)
		}
	}
	return out
}

// Resource returns def + handler for a concrete resource URI.
func (b *statelessBackend) Resource(uri string) (core.ResourceDef, core.ResourceHandler, bool) {
	r := b.reg()
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.resources[uri]
	if !ok {
		return core.ResourceDef{}, nil, false
	}
	return e.def, e.handler, true
}

// ResourceTemplates returns a snapshot of templates in registration order.
func (b *statelessBackend) ResourceTemplates() []core.ResourceTemplate {
	r := b.reg()
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]core.ResourceTemplate, 0, len(r.templateOrder))
	for _, uri := range r.templateOrder {
		if e, ok := r.templates[uri]; ok {
			out = append(out, e.def)
		}
	}
	return out
}

// ResourceTemplate returns def + handler for a template URI.
func (b *statelessBackend) ResourceTemplate(uriTemplate string) (core.ResourceTemplate, core.TemplateHandler, bool) {
	r := b.reg()
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.templates[uriTemplate]
	if !ok {
		return core.ResourceTemplate{}, nil, false
	}
	return e.def, e.handler, true
}

// Prompts returns a snapshot of prompts in registration order.
func (b *statelessBackend) Prompts() []core.PromptDef {
	r := b.reg()
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]core.PromptDef, 0, len(r.promptOrder))
	for _, name := range r.promptOrder {
		if e, ok := r.prompts[name]; ok {
			out = append(out, e.def)
		}
	}
	return out
}

// Prompt returns def + handler for a prompt name.
func (b *statelessBackend) Prompt(name string) (core.PromptDef, core.PromptHandler, bool) {
	r := b.reg()
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.prompts[name]
	if !ok {
		return core.PromptDef{}, nil, false
	}
	return e.def, e.handler, true
}

// Completion returns a registered completion handler for the
// (refType, name|uri) pair. Registry keys completions as
// "refType:key" — this adapter joins them on read.
func (b *statelessBackend) Completion(refType, key string) (core.CompletionHandler, bool) {
	r := b.reg()
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.completions[refType+":"+key]
	return h, ok
}

// ListTTLMs returns the SEP-2549 ttlMs hint applied to list responses. The
// stateless wire speaks only 2026-07-28, where the field is required, so a
// hint the server turned off comes back as 0 (#1546).
func (b *statelessBackend) ListTTLMs() *int {
	ttlMs, _ := cacheHints(statelessFeatures, b.s.options.listTTLMs, b.s.options.listCacheScope, core.CacheScopePublic)
	return ttlMs
}

// ListCacheScope returns the SEP-2549 cacheScope hint applied to list
// responses, "public" when the server turned it off (see ListTTLMs).
func (b *statelessBackend) ListCacheScope() string {
	_, scope := cacheHints(statelessFeatures, b.s.options.listTTLMs, b.s.options.listCacheScope, core.CacheScopePublic)
	return scope
}

// MatchResourceTemplate matches uri against registered templates in
// registration order, mirroring Dispatcher.handleResourcesRead on the session
// wire so a URI resolves to the same definition on both.
func (b *statelessBackend) MatchResourceTemplate(uri string) (core.ResourceTemplate, core.TemplateHandler, map[string]string, bool) {
	r := b.s.Registry()
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, tmplURI := range r.templateOrder {
		entry := r.templates[tmplURI]
		if params, ok := matchTemplate(entry.def.URITemplate, uri); ok {
			return entry.def, entry.handler, params, true
		}
	}
	return core.ResourceTemplate{}, nil, nil, false
}

// ReadTTLMs returns the SEP-2549 ttlMs default for resources/read responses,
// 0 when the server turned it off (see ListTTLMs).
func (b *statelessBackend) ReadTTLMs() *int {
	ttlMs, _ := cacheHints(statelessFeatures, b.s.options.readTTLMs, b.s.options.readCacheScope, core.CacheScopePrivate)
	return ttlMs
}

// ReadCacheScope returns the SEP-2549 cacheScope default for resources/read
// responses, "private" when the server turned it off (see ListTTLMs).
func (b *statelessBackend) ReadCacheScope() string {
	_, scope := cacheHints(statelessFeatures, b.s.options.readTTLMs, b.s.options.readCacheScope, core.CacheScopePrivate)
	return scope
}

// InvokeWithMiddleware runs the server's middleware chain (s.options.middleware)
// around a terminal handler that dispatches by method:
//
//   - "tools/call", "prompts/get", "resources/read", "completion/complete" →
//     the session dispatcher's own handlers, called with wireStateless, so
//     every check and timeout they run applies here too (#1637). Where the
//     wires differ, the handler branches on wire.
//   - the four */list methods → answer from the registry, as the session
//     dispatcher does, so a middleware sees and can rewrite them on this wire
//     too (#1355).
//   - any other method → consult s.dispatcher.customHandlers (the map populated
//     by Server.HandleMethod, used by tasks.Register for tasks/get|update|cancel).
//     Returns -32601 when no handler is registered for the method.
//
// The middleware chain matters because the v2 tasks extension installs
// taskV2Middleware via Server.UseMiddleware; without traversing that chain
// the stateless wire's tools/call would never produce a CreateTaskResult.
//
// Background goroutines spawned from middleware (e.g., the v2 task runner)
// have no session-level notify path on the stateless wire — there's no
// persistent GET SSE stream to push notifications onto. notifications/tasks
// emissions silently drop, which matches the SEP-2575 design (no server-
// initiated push); clients observe state via tasks/get polling.
//
// By default, stateless task store entries key under sessionID="" (no
// session), so stateless tasks share one bucket per process — fine for the
// single-tenant fixtures the conformance suite covers. Multi-tenant
// deployments install a server.WithTaskBucketKeyer that derives the bucket
// from an authenticated subject; the keyer is injected onto the request
// context in the stateless POST handlers (streamable_stateless.go) and the
// v1/v2 task surfaces resolve it via core.TaskBucketKey (issue 485).
func (b *statelessBackend) InvokeWithMiddleware(ctx context.Context, req *core.Request) (*core.Response, error, bool) {
	terminal := MiddlewareFunc(func(ctx context.Context, req *core.Request) (*core.Response, error) {
		switch req.Method {
		case "tools/call":
			return b.s.dispatcher.handleToolsCall(ctx, wireStateless, req.ID, req.Params.Raw()), nil
		case "prompts/get":
			return b.s.dispatcher.handlePromptsGet(ctx, req.ID, req.Params.Raw()), nil
		case "resources/read":
			return b.s.dispatcher.handleResourcesRead(ctx, wireStateless, req.ID, req.Params.Raw()), nil
		case "tools/list":
			// The stateless wire reads fileInputs from the request's _meta (#1515).
			return core.NewResponse(req.ID, toolsListResult(b.reg(), req.Params.Raw(), core.HasFileInputs(ctx), b.ListTTLMs(), b.ListCacheScope())), nil
		case "resources/list":
			return core.NewResponse(req.ID, core.ResourcesListResult{Resources: b.Resources(), TTLMs: b.ListTTLMs(), CacheScope: b.ListCacheScope()}), nil
		case "resources/templates/list":
			return core.NewResponse(req.ID, core.ResourceTemplatesListResult{ResourceTemplates: b.ResourceTemplates(), TTLMs: b.ListTTLMs(), CacheScope: b.ListCacheScope()}), nil
		case "prompts/list":
			return core.NewResponse(req.ID, core.PromptsListResult{Prompts: b.Prompts(), TTLMs: b.ListTTLMs(), CacheScope: b.ListCacheScope()}), nil
		case "completion/complete":
			return b.s.dispatcher.handleCompletionComplete(ctx, req.ID, req.Params.Raw()), nil
		default:
			if h, ok := b.s.dispatcher.customHandlers[req.Method]; ok {
				return h(core.NewMethodContext(ctx), req.ID, req.Params.Raw()), nil
			}
			return core.NewErrorResponse(req.ID, core.ErrCodeMethodNotFound,
				"method not found: "+req.Method), nil
		}
	})

	handler := terminal
	for i := len(b.s.options.middleware) - 1; i >= 0; i-- {
		next := handler
		mw := b.s.options.middleware[i]
		handler = func(ctx context.Context, req *core.Request) (*core.Response, error) {
			return mw(ctx, req, next)
		}
	}

	resp, err := handler(ctx, req)
	if err != nil {
		// Surface the raw middleware error (typically *core.AuthError) so the
		// transport's writeAuthError can emit the correct HTTP status +
		// WWW-Authenticate header — mirroring the legacy wire. Folding it into
		// a generic -32603 here would drop the 403 + scope challenge (issue 815).
		return nil, err, true
	}
	return resp, nil, true
}
