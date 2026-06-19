package stateless

import (
	"context"
	"encoding/json"
	"errors"

	core "github.com/panyam/mcpkit/core"
)

// SEP-2575 per-method handlers. Kept small and intentionally cap-blind:
// per-request capability gating that the conformance suite exercises
// (the -32021 path) is emitted by tool handlers themselves via a typed
// *core.MissingCapabilityError that this dispatcher translates at the
// tools/call boundary. Keeps the dispatcher logic per-method-uniform.

// ---------- tools ----------

func (d *Dispatcher) handleToolsList(id json.RawMessage, _ json.RawMessage) *core.Response {
	tools := d.Backend.Tools()
	// Cap-aware stripping (SEP-2356 x-mcp-file etc.) is the legacy
	// dispatcher's concern; stateless clients always advertise caps
	// per-request so any stripping happens against the per-call envelope.
	// First-cut emits the unfiltered list; per-request cap stripping
	// lands when the example fixture starts exercising the path.
	return core.NewResponse(id, core.ToolsListResult{
		Tools:      tools,
		NextCursor: "",
		TTLMs:      d.Backend.ListTTLMs(),
		CacheScope: d.Backend.ListCacheScope(),
	})
}

// toolsCallEnvelope is the stateless wire's tools/call params shape.
// Mirrors the legacy envelope (server/mrtr.go) field-for-field; defined
// here so the stateless dispatcher does not import server-internal types.
// The two definitions converge if/when the legacy wire is retired.
type toolsCallEnvelope struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	// _meta progressToken etc. ride alongside the SEP-2575 envelope; the
	// dispatcher does not consume them here.
}

func (d *Dispatcher) handleToolsCall(ctx context.Context, id json.RawMessage, params json.RawMessage) *core.Response {
	// Prefer the middleware-aware path so server-level middleware (notably
	// the v2 task middleware from ext/tasks) fires on the stateless wire
	// just like it does on the legacy wire. Backends that don't carry
	// middleware (test fakes) return ok=false and we fall back to direct
	// invocation below.
	req := &core.Request{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "tools/call",
		Params:  params,
	}
	if resp, ok := d.Backend.InvokeWithMiddleware(ctx, req); ok {
		return resp
	}

	// Fallback path: no middleware support on this backend.
	var env toolsCallEnvelope
	if err := json.Unmarshal(params, &env); err != nil {
		return core.NewErrorResponse(id, core.ErrCodeInvalidParams,
			"invalid tools/call params: "+err.Error())
	}
	def, handler, ok := d.Backend.Tool(env.Name)
	if !ok {
		return core.NewErrorResponse(id, core.ErrCodeInvalidParams,
			"unknown tool: "+env.Name)
	}
	_ = def // tool definition reserved for schema validation in a follow-up commit

	toolCtx := core.NewToolContext(ctx)
	result, err := handler(toolCtx, core.ToolRequest{
		Name:      env.Name,
		Arguments: env.Arguments,
	})
	if err != nil {
		return translateToolError(id, err)
	}
	return core.NewResponse(id, result)
}

// translateToolError converts a tool-handler error into the right
// JSON-RPC error shape. Typed *core.MissingCapabilityError becomes
// the SEP-2575 -32021 + structured requiredCapabilities payload so
// the conformance "ServerRejectsUndeclaredCapability" check passes.
// Everything else falls back to -31000 ErrCodeToolExecutionError.
func translateToolError(id json.RawMessage, err error) *core.Response {
	var missing *core.MissingCapabilityError
	if errors.As(err, &missing) {
		return core.NewErrorResponseWithData(
			id,
			core.ErrCodeMissingRequiredClientCapability,
			missing.Error(),
			core.MissingRequiredClientCapabilityData{
				RequiredCapabilities: missing.Required,
			},
		)
	}
	return core.NewErrorResponse(id, core.ErrCodeToolExecutionError, err.Error())
}

// ---------- resources ----------

func (d *Dispatcher) handleResourcesList(id json.RawMessage, _ json.RawMessage) *core.Response {
	return core.NewResponse(id, core.ResourcesListResult{
		Resources:  d.Backend.Resources(),
		NextCursor: "",
		TTLMs:      d.Backend.ListTTLMs(),
		CacheScope: d.Backend.ListCacheScope(),
	})
}

type resourcesReadEnvelope struct {
	URI string `json:"uri"`
}

func (d *Dispatcher) handleResourcesRead(ctx context.Context, id json.RawMessage, params json.RawMessage) *core.Response {
	var env resourcesReadEnvelope
	if err := json.Unmarshal(params, &env); err != nil {
		return core.NewErrorResponse(id, core.ErrCodeInvalidParams,
			"invalid resources/read params: "+err.Error())
	}
	_, handler, ok := d.Backend.Resource(env.URI)
	if !ok {
		// Concrete URIs miss → try templates. The legacy dispatcher does
		// the same match-then-template fallback; we replicate just the
		// match path for first-cut, deferring template matching to a
		// follow-up commit alongside the example fixture's templated
		// resources.
		return core.NewErrorResponse(id, core.ErrCodeInvalidParams,
			"unknown resource: "+env.URI)
	}
	result, err := handler(core.NewResourceContext(ctx), core.ResourceRequest{URI: env.URI})
	if err != nil {
		return core.NewErrorResponse(id, core.ErrCodeResourceError, err.Error())
	}
	return core.NewResponse(id, result)
}

func (d *Dispatcher) handleResourcesTemplatesList(id json.RawMessage, _ json.RawMessage) *core.Response {
	return core.NewResponse(id, core.ResourceTemplatesListResult{
		ResourceTemplates: d.Backend.ResourceTemplates(),
		NextCursor:        "",
		TTLMs:             d.Backend.ListTTLMs(),
		CacheScope:        d.Backend.ListCacheScope(),
	})
}

// ---------- prompts ----------

func (d *Dispatcher) handlePromptsList(id json.RawMessage, _ json.RawMessage) *core.Response {
	return core.NewResponse(id, core.PromptsListResult{
		Prompts:    d.Backend.Prompts(),
		NextCursor: "",
		TTLMs:      d.Backend.ListTTLMs(),
		CacheScope: d.Backend.ListCacheScope(),
	})
}

type promptsGetEnvelope struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

func (d *Dispatcher) handlePromptsGet(ctx context.Context, id json.RawMessage, params json.RawMessage) *core.Response {
	// Prefer the middleware-aware path so server-level middleware fires
	// uniformly on the stateless wire AND so the MRTR envelope
	// (inputResponses + requestState) is decoded and the requestState
	// minted by the backend's shared mrtrRuntime. Backends that don't
	// carry middleware return ok=false and we fall back to the direct
	// invocation below — used by minimal test fakes only.
	req := &core.Request{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "prompts/get",
		Params:  params,
	}
	if resp, ok := d.Backend.InvokeWithMiddleware(ctx, req); ok {
		return resp
	}

	// Fallback path (test fakes with no middleware support): no MRTR
	// envelope handling, no requestState signing — just look up the
	// prompt and invoke directly.
	var env promptsGetEnvelope
	if err := json.Unmarshal(params, &env); err != nil {
		return core.NewErrorResponse(id, core.ErrCodeInvalidParams,
			"invalid prompts/get params: "+err.Error())
	}
	_, handler, ok := d.Backend.Prompt(env.Name)
	if !ok {
		return core.NewErrorResponse(id, core.ErrCodeInvalidParams,
			"unknown prompt: "+env.Name)
	}
	result, err := handler(core.NewPromptContext(ctx), core.PromptRequest{
		Name:      env.Name,
		Arguments: env.Arguments,
	})
	if err != nil {
		return core.NewErrorResponse(id, core.ErrCodePromptError, err.Error())
	}
	return core.NewResponse(id, result)
}

// ---------- completion ----------

type completionCompleteEnvelope struct {
	Ref      core.CompletionRef      `json:"ref"`
	Argument core.CompletionArgument `json:"argument"`
}

func (d *Dispatcher) handleCompletionComplete(ctx context.Context, id json.RawMessage, params json.RawMessage) *core.Response {
	var env completionCompleteEnvelope
	if err := json.Unmarshal(params, &env); err != nil {
		return core.NewErrorResponse(id, core.ErrCodeInvalidParams,
			"invalid completion/complete params: "+err.Error())
	}
	// Completion is keyed by (refType, name | uri) — name for ref/prompt,
	// uri for ref/resource. Coalesce the two so backends don't have to.
	key := env.Ref.Name
	if env.Ref.Type == "ref/resource" {
		key = env.Ref.URI
	}
	handler, ok := d.Backend.Completion(env.Ref.Type, key)
	if !ok {
		// Spec allows completion to return an empty list when no handler
		// is registered for the (refType,name) pair. Stay on the lenient
		// side — the conformance suite does not exercise this miss path
		// but legacy callers expect a non-error response.
		return core.NewResponse(id, core.CompletionCompleteResult{
			Completion: core.CompletionResult{Values: []string{}},
		})
	}
	result, err := handler(core.NewPromptContext(ctx), env.Ref, env.Argument)
	if err != nil {
		return core.NewErrorResponse(id, core.ErrCodeCompletionError, err.Error())
	}
	return core.NewResponse(id, core.CompletionCompleteResult{Completion: result})
}
