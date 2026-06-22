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
				Config:      e.Config,
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
//     *core.MissingCapabilityError into a SEP-2575 -32021 response.
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
func (b *statelessBackend) InvokeWithMiddleware(ctx context.Context, req *core.Request) (*core.Response, error, bool) {
	terminal := MiddlewareFunc(func(ctx context.Context, req *core.Request) (*core.Response, error) {
		switch req.Method {
		case "tools/call":
			return b.callToolForStateless(ctx, req), nil
		case "prompts/get":
			return b.callPromptForStateless(ctx, req), nil
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
		// Surface the raw middleware error (typically *core.AuthError) so the
		// transport's writeAuthError can emit the correct HTTP status +
		// WWW-Authenticate header — mirroring the legacy wire. Folding it into
		// a generic -32603 here would drop the 403 + scope challenge (issue 815).
		return nil, err, true
	}
	return resp, nil, true
}

// callToolForStateless mirrors the legacy Dispatcher.handleToolsCall MRTR
// flow on the stateless wire: decode the full SEP-2322 envelope
// (inputResponses + requestState alongside the tool name + arguments),
// verify the echoed requestState through the shared mrtrRuntime, merge
// accumulated answers from prior rounds into the current call, populate
// ToolContext with the merged view, and reshape any handler-returned
// InputRequiredResult into a wire response that carries a freshly-minted
// requestState. Without this parity the stateless tools/call path strips
// every MRTR field on entry, which is why upstream's input-required-result-*
// conformance scenarios fail with "Expected InputRequiredResult..." before
// this change.
//
// progressToken is read off the same envelope (params._meta.progressToken),
// not the SEP-2575 _meta envelope (which is for protocolVersion / clientInfo
// / clientCapabilities). The stateless dispatcher's _meta validation runs
// upstream of this call so we don't re-validate here.
func (b *statelessBackend) callToolForStateless(ctx context.Context, req *core.Request) *core.Response {
	var env toolsCallEnvelope
	if err := json.Unmarshal(req.Params, &env); err != nil {
		return core.NewErrorResponse(req.ID, core.ErrCodeInvalidParams,
			"invalid tools/call params: "+err.Error())
	}
	_, handler, ok := b.Tool(env.Name)
	if !ok {
		return core.NewErrorResponse(req.ID, core.ErrCodeInvalidParams,
			"unknown tool: "+env.Name)
	}

	// SEP-2322: verify the echoed requestState (rejects tampered or expired
	// tokens before the handler ever runs) and pull out the accumulated
	// inputResponses carried inside it from prior rounds.
	prevState, err := b.s.dispatcher.mrtr.verifyRequestState(env.RequestState, env.Name)
	if err != nil {
		return core.NewErrorResponse(req.ID, core.ErrCodeInvalidParams,
			"invalid requestState: "+err.Error())
	}
	mergedResponses := mergeInputResponses(prevState.Answered, env.InputResponses)

	var progressToken any
	if env.Meta != nil {
		progressToken = env.Meta.ProgressToken
	}

	tc := core.NewToolContextWithMRTR(ctx, progressToken, mergedResponses, env.RequestState)
	result, err := handler(tc, core.ToolRequest{
		Name:           env.Name,
		Arguments:      env.Arguments,
		RequestID:      req.ID,
		InputResponses: mergedResponses,
		RequestState:   env.RequestState,
		ProgressToken:  progressToken,
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

	// SEP-2322 reshape: an InputRequiredResult variant gets a freshly-minted
	// requestState carrying the merged accumulated answers so the next round
	// sees them too. Every other ToolResponse flows through as-is.
	switch r := result.(type) {
	case core.InputRequiredResult:
		return core.NewResponse(req.ID, core.InputRequiredResult{
			InputRequests: r.InputRequests,
			RequestState:  b.s.dispatcher.mrtr.mintRequestState(env.Name, mergedResponses),
		})
	default:
		return core.NewResponse(req.ID, result)
	}
}

// callPromptForStateless mirrors callToolForStateless above on the
// prompts/get surface: decode the SEP-2322 MRTR envelope, verify+merge
// requestState via the shared mrtrRuntime, populate PromptContext with
// the MRTR view, and reshape any handler-returned InputRequiredResult
// (PromptResponse variant) with a freshly-minted requestState. SEP-2322
// scopes the input-required flow to any request method whose response
// shape can accept it — prompts/get is the canonical second surface and
// the conformance scenario `input-required-result-non-tool-request`
// exercises it.
func (b *statelessBackend) callPromptForStateless(ctx context.Context, req *core.Request) *core.Response {
	var env promptsGetEnvelope
	if err := json.Unmarshal(req.Params, &env); err != nil {
		return core.NewErrorResponse(req.ID, core.ErrCodeInvalidParams,
			"invalid prompts/get params: "+err.Error())
	}
	_, handler, ok := b.Prompt(env.Name)
	if !ok {
		return core.NewErrorResponse(req.ID, core.ErrCodeInvalidParams,
			"unknown prompt: "+env.Name)
	}

	prevState, err := b.s.dispatcher.mrtr.verifyRequestState(env.RequestState, env.Name)
	if err != nil {
		return core.NewErrorResponse(req.ID, core.ErrCodeInvalidParams,
			"invalid requestState: "+err.Error())
	}
	mergedResponses := mergeInputResponses(prevState.Answered, env.InputResponses)

	pc := core.NewPromptContextWithMRTR(ctx, mergedResponses, env.RequestState)
	result, err := handler(pc, core.PromptRequest{
		Name:           env.Name,
		Arguments:      env.Arguments,
		InputResponses: mergedResponses,
		RequestState:   env.RequestState,
	})
	if err != nil {
		return core.NewErrorResponse(req.ID, core.ErrCodePromptError, err.Error())
	}

	switch r := result.(type) {
	case core.InputRequiredResult:
		return core.NewResponse(req.ID, core.InputRequiredResult{
			InputRequests: r.InputRequests,
			RequestState:  b.s.dispatcher.mrtr.mintRequestState(env.Name, mergedResponses),
		})
	default:
		return core.NewResponse(req.ID, result)
	}
}
