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

// resources returns a snapshot of concrete resources in registration order.
func (b *statelessBackend) resources() []core.ResourceDef {
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

// resourceTemplates returns a snapshot of templates in registration order.
func (b *statelessBackend) resourceTemplates() []core.ResourceTemplate {
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

// prompts returns a snapshot of prompts in registration order.
func (b *statelessBackend) prompts() []core.PromptDef {
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

// listTTLMs returns the SEP-2549 ttlMs hint applied to list responses. The
// stateless wire speaks only 2026-07-28, where the field is required, so a
// hint the server turned off comes back as 0 (#1546).
func (b *statelessBackend) listTTLMs() *int {
	ttlMs, _ := cacheHints(statelessFeatures, b.s.options.listTTLMs, b.s.options.listCacheScope, core.CacheScopePublic)
	return ttlMs
}

// listCacheScope returns the SEP-2549 cacheScope hint applied to list
// responses, "public" when the server turned it off (see listTTLMs).
func (b *statelessBackend) listCacheScope() string {
	_, scope := cacheHints(statelessFeatures, b.s.options.listTTLMs, b.s.options.listCacheScope, core.CacheScopePublic)
	return scope
}

// InvokeWithMiddleware runs req through Server.runRequest with wireStateless,
// so the stateless wire gets the same per-request context, user middleware,
// metrics and trace middleware as the session wire (#1625). The stateless
// dispatcher calls it for every method past its envelope checks. The
// terminal dispatches by method:
//
//   - "server/discover" → the discover result, answered inside the chain so a
//     middleware sees it the way it sees initialize on the session wire.
//   - "tools/call", "prompts/get", "resources/read", "completion/complete" →
//     the session dispatcher's own handlers, called with wireStateless, so
//     every check and timeout they run applies here too (#1637). Where the
//     wires differ, the handler branches on wire.
//   - the four */list methods → answer from the registry, as the session
//     dispatcher does (#1355).
//   - any other method → s.dispatcher.customHandlers (Server.HandleMethod,
//     used by tasks.Register for tasks/get|update|cancel), or -32601.
//
// The terminal recovers a handler panic into -32603, as the session wire's
// Dispatch does.
//
// Background goroutines spawned from middleware (e.g., the v2 task runner)
// have no session-level notify path on the stateless wire — there's no
// persistent GET SSE stream to push notifications onto. notifications/tasks
// emissions silently drop, which matches the SEP-2575 design (no server-
// initiated push); clients observe state via tasks/get polling.
//
// By default, stateless task store entries key under sessionID="" (no
// session), so stateless tasks share one bucket per process. Multi-tenant
// deployments install a server.WithTaskBucketKeyer that derives the bucket
// from an authenticated subject; runRequest puts it on ctx for both wires
// (issue 485).
//
// A non-nil error is a middleware short-circuit (typically *core.AuthError),
// returned raw so the transport's writeAuthError emits the same HTTP status +
// WWW-Authenticate as the session wire (issue 815).
func (b *statelessBackend) InvokeWithMiddleware(ctx context.Context, req *core.Request) (*core.Response, error) {
	d := b.s.dispatcher
	return b.s.runRequest(ctx, wireStateless, d, req, sessionIO{},
		func(ctx context.Context, req *core.Request) (resp *core.Response, err error) {
			defer recoverHandlerPanic(req, &resp)
			switch req.Method {
			case "server/discover":
				return core.NewResponse(req.ID, stateless.NewDiscoverResult(b)), nil
			case "tools/call":
				return d.handleToolsCall(ctx, wireStateless, req.ID, req.Params.Raw()), nil
			case "prompts/get":
				return d.handlePromptsGet(ctx, req.ID, req.Params.Raw()), nil
			case "resources/read":
				return d.handleResourcesRead(ctx, wireStateless, req.ID, req.Params.Raw()), nil
			case "tools/list":
				// The stateless wire reads fileInputs from the request's _meta (#1515).
				return core.NewResponse(req.ID, toolsListResult(b.reg(), req.Params.Raw(), core.HasFileInputs(ctx), b.listTTLMs(), b.listCacheScope())), nil
			case "resources/list":
				return core.NewResponse(req.ID, core.ResourcesListResult{Resources: b.resources(), TTLMs: b.listTTLMs(), CacheScope: b.listCacheScope()}), nil
			case "resources/templates/list":
				return core.NewResponse(req.ID, core.ResourceTemplatesListResult{ResourceTemplates: b.resourceTemplates(), TTLMs: b.listTTLMs(), CacheScope: b.listCacheScope()}), nil
			case "prompts/list":
				return core.NewResponse(req.ID, core.PromptsListResult{Prompts: b.prompts(), TTLMs: b.listTTLMs(), CacheScope: b.listCacheScope()}), nil
			case "completion/complete":
				return d.handleCompletionComplete(ctx, req.ID, req.Params.Raw()), nil
			default:
				if h, ok := d.customHandlers[req.Method]; ok {
					return h(core.NewMethodContext(ctx), req.ID, req.Params.Raw()), nil
				}
				return core.NewErrorResponse(req.ID, core.ErrCodeMethodNotFound,
					"method not found: "+req.Method), nil
			}
		})
}
