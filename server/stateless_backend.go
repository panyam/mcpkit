package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

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

// ServerInfo returns the implementation identity advertised in
// server/discover responses. Sourced from NewServer's first argument.
func (b *statelessBackend) ServerInfo() core.ServerInfo {
	return b.s.dispatcher.serverInfo
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
			caps.Extensions[id] = core.ExtensionCapability{
				SpecVersion: e.SpecVersion,
				Stability:   string(e.Stability),
			}
		}
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

// ListTTLMs returns the SEP-2549 ttlMs hint applied to list responses.
func (b *statelessBackend) ListTTLMs() *int {
	return b.s.options.listTTLMs
}

// ListCacheScope returns the SEP-2549 cacheScope hint applied to list responses.
func (b *statelessBackend) ListCacheScope() string {
	return b.s.options.listCacheScope
}

// InvokeWithMiddleware runs the server's middleware chain (s.options.middleware)
// around a terminal handler that dispatches by method:
//
//   - "tools/call" → look up the registered tool, invoke it, translate
//     *core.MissingCapabilityError into a SEP-2575 -32003 response.
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
// All stateless task store entries currently key under sessionID=""
// (no session). This means stateless tasks share one bucket per process,
// which is acceptable for the single-tenant fixtures the conformance
// suite covers; multi-tenant deployments should layer an auth-subject-
// keyed store wrapper. Tracked for a follow-up.
func (b *statelessBackend) InvokeWithMiddleware(ctx context.Context, req *core.Request) (*core.Response, bool) {
	terminal := MiddlewareFunc(func(ctx context.Context, req *core.Request) (*core.Response, error) {
		switch req.Method {
		case "tools/call":
			return b.callToolForStateless(ctx, req), nil
		default:
			if h, ok := b.s.dispatcher.customHandlers[req.Method]; ok {
				return h(core.NewMethodContext(ctx), req.ID, req.Params), nil
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
		return core.NewErrorResponse(req.ID, core.ErrCodeInternal, err.Error()), true
	}
	return resp, true
}

// callToolForStateless replicates the decode → look-up → invoke → translate
// flow that stateless.handleToolsCall used to do directly. Kept here so the
// stateless dispatcher's per-method file stays thin and the middleware-aware
// path lives next to the rest of the Backend impl.
func (b *statelessBackend) callToolForStateless(ctx context.Context, req *core.Request) *core.Response {
	var env struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments,omitempty"`
	}
	if err := json.Unmarshal(req.Params, &env); err != nil {
		return core.NewErrorResponse(req.ID, core.ErrCodeInvalidParams,
			"invalid tools/call params: "+err.Error())
	}
	_, handler, ok := b.Tool(env.Name)
	if !ok {
		return core.NewErrorResponse(req.ID, core.ErrCodeInvalidParams,
			"unknown tool: "+env.Name)
	}
	result, err := handler(core.NewToolContext(ctx), core.ToolRequest{
		Name:      env.Name,
		Arguments: env.Arguments,
	})
	if err != nil {
		var missing *core.MissingCapabilityError
		if errors.As(err, &missing) {
			return core.NewErrorResponseWithData(
				req.ID,
				core.ErrCodeMissingRequiredClientCapability,
				missing.Error(),
				core.MissingRequiredClientCapabilityData{
					RequiredCapabilities: missing.Required,
				},
			)
		}
		// Plain handler error becomes isError content in a SUCCESS JSON-RPC
		// response, matching the legacy dispatch path (server/dispatch.go
		// handleToolsCall). For SEP-2663 task-creating calls this is load-
		// bearing: the v2 task middleware sees resp.Error == nil and stores
		// the task as `completed` with isError, per spec — not as `failed`.
		result = core.ErrorResult(fmt.Sprintf("tool %q: %v", env.Name, err))
	}
	return core.NewResponse(req.ID, result)
}
