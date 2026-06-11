package core

import (
	"context"
	"encoding/json"
	"time"
)

// BaseContext provides typed access to session capabilities shared by all
// MCP handler types (tools, resources, prompts, completions). It embeds
// context.Context so stdlib functions (Done(), Deadline(), WithTimeout)
// work transparently.
//
// All methods are safe to call even when the underlying capability is
// unavailable (e.g., EmitLog on a transport without logging is a no-op).
type BaseContext struct {
	context.Context
	sc *sessionCtx
}

// ToolContext is the context passed to ToolHandler functions. It embeds
// BaseContext and adds tool-specific methods (EmitProgress, EmitContent).
type ToolContext struct {
	BaseContext
	progressToken any // set by dispatch or TypedTool from _meta.progressToken

	// inputResponses + requestState carry the SEP-2322 ephemeral MRTR
	// continuation payload — the client echoes them back into the SAME
	// tools/call request when retrying after a previous InputRequiredResult.
	// Set by dispatch from the request envelope; nil/empty on the first call.
	inputResponses InputResponses
	requestState   string
}

// ResourceContext is the context passed to ResourceHandler and
// TemplateHandler functions. It embeds BaseContext with no additional
// methods — resources don't support progress or content streaming.
type ResourceContext struct {
	BaseContext
}

// PromptContext is the context passed to PromptHandler and
// CompletionHandler functions. It embeds BaseContext and adds SEP-2322
// MRTR accessors symmetric with ToolContext — a prompts/get handler can
// branch on `ctx.HasInputResponses()`, return `ctx.RequestInput(...)` on
// the first call, and decode `ctx.InputResponse("key")` on the second.
type PromptContext struct {
	BaseContext

	// inputResponses + requestState carry the SEP-2322 ephemeral MRTR
	// continuation payload — the client echoes them back into the SAME
	// prompts/get request when retrying after a previous
	// InputRequiredResult. Set by dispatch from the request envelope;
	// nil/empty on the first call.
	inputResponses InputResponses
	requestState   string
}

// MethodContext is the context passed to custom JSON-RPC method handlers
// registered via server.HandleMethod or server.WithMethodHandler. It embeds
// BaseContext with no additional methods — custom methods get the same
// session capabilities as other handlers (EmitLog, Sample, Elicit, etc.).
type MethodContext struct {
	BaseContext
}

// NewMethodContext constructs a MethodContext from a standard context.Context.
func NewMethodContext(ctx context.Context) MethodContext {
	return MethodContext{BaseContext{ctx, sessionFromContext(ctx)}}
}

// DetachFromClient returns a MethodContext that preserves session state but is
// NOT cancelled when the client disconnects.
func (mc MethodContext) DetachFromClient() MethodContext {
	return MethodContext{mc.BaseContext.DetachFromClient()}
}

// NewToolContext constructs a ToolContext from a standard context.Context.
// Called by the dispatch layer before invoking tool handlers.
func NewToolContext(ctx context.Context) ToolContext {
	return ToolContext{BaseContext: BaseContext{ctx, sessionFromContext(ctx)}}
}

// NewToolContextWithProgress constructs a ToolContext with a stored progress
// token. The dispatch layer passes the token from _meta.progressToken so that
// handlers can call ctx.Progress() without threading the token manually.
func NewToolContextWithProgress(ctx context.Context, progressToken any) ToolContext {
	return ToolContext{
		BaseContext:   BaseContext{ctx, sessionFromContext(ctx)},
		progressToken: progressToken,
	}
}

// NewToolContextWithMRTR constructs a ToolContext for an SEP-2322 MRTR retry:
// progress token plus the inputResponses/requestState the client echoed back.
// Called by the tools/call dispatch layer after parsing the request envelope;
// handlers read the values via ctx.InputResponse / ctx.InputResponses /
// ctx.RequestState.
func NewToolContextWithMRTR(ctx context.Context, progressToken any, inputResponses InputResponses, requestState string) ToolContext {
	return ToolContext{
		BaseContext:    BaseContext{ctx, sessionFromContext(ctx)},
		progressToken:  progressToken,
		inputResponses: inputResponses,
		requestState:   requestState,
	}
}

// NewResourceContext constructs a ResourceContext from a standard context.Context.
func NewResourceContext(ctx context.Context) ResourceContext {
	return ResourceContext{BaseContext{ctx, sessionFromContext(ctx)}}
}

// NewPromptContext constructs a PromptContext from a standard context.Context.
func NewPromptContext(ctx context.Context) PromptContext {
	return PromptContext{BaseContext: BaseContext{ctx, sessionFromContext(ctx)}}
}

// NewPromptContextWithMRTR constructs a PromptContext for an SEP-2322 MRTR
// retry: the inputResponses/requestState the client echoed back into the
// prompts/get request. Called by the dispatch layer after parsing the
// envelope; handlers read the values via ctx.InputResponse / ctx.InputResponses
// / ctx.RequestState.
func NewPromptContextWithMRTR(ctx context.Context, inputResponses InputResponses, requestState string) PromptContext {
	return PromptContext{
		BaseContext:    BaseContext{ctx, sessionFromContext(ctx)},
		inputResponses: inputResponses,
		requestState:   requestState,
	}
}

// ClientCaps returns the client capabilities the handler should gate
// against for THIS request. SEP-2322 says servers MUST only emit
// inputRequests for methods the client declared support for; the rule
// is the same on both wires, but the source of truth differs:
//
//   - Legacy wire: capabilities are negotiated once during `initialize`
//     and cached on the session.
//   - Stateless wire (SEP-2575): no session — capabilities are declared
//     per-request inside the _meta envelope, fresh on every call.
//
// This accessor coalesces the two into a single typed view so handlers
// don't have to special-case the wire. On the legacy wire it returns
// the session-cached caps; on the stateless wire it returns the per-
// request envelope's caps (which is the only source there). Either
// pointer may be nil — handlers MUST nil-check before reading sub-
// capabilities.
//
// Usage in a tool handler that wants to skip elicitation inputRequests
// when the client did not declare elicitation:
//
//	caps := ctx.ClientCaps()
//	if caps != nil && caps.Elicitation != nil {
//	    reqs["user_name"] = core.InputRequest{Method: "elicitation/create", ...}
//	}
func (bc BaseContext) ClientCaps() *ClientCapabilities {
	if meta := RequestMetaFromContext(bc.Context); meta != nil {
		return meta.ClientCapabilities
	}
	if bc.sc != nil {
		return bc.sc.clientCaps
	}
	return nil
}

// Span returns the currently active mcpkit Span for this request, or a
// no-op Span when no TracerProvider has been wired. The returned Span
// is NEVER nil — handlers can unconditionally call SetAttribute /
// RecordError without nil-checking.
//
// This is the symmetric sibling of TraceContext() above: TraceContext()
// surfaces the W3C trace identity propagated over the wire; Span()
// surfaces the local in-process span the trace middleware started
// around this dispatch.
//
// The enrichment pattern:
//
//	ctx.Span().SetAttribute("mcp.auth.principal", ctx.AuthClaims().Subject)
//
// Prefer this when you want to decorate the existing dispatch span with
// extra attributes — e.g., an auth middleware adding principal info, a
// tool handler adding result-shape hints. Use a TracerProvider directly
// (StartSpan + child span) when you want a separate finer-grained span
// instead.
//
// Always cheap — a single ctx.Value lookup. Safe for concurrent use.
func (bc BaseContext) Span() Span {
	return SpanFromContext(bc.Context)
}

// TraceContext returns the active W3C Trace Context for this request, or
// a zero TraceContext when none has been attached.
//
// The dispatch layer is responsible for extracting `_meta.traceparent` /
// `_meta.tracestate` from inbound requests and attaching the result via
// core.WithTraceContext before invoking the handler — the same plumbing
// pattern as ProgressToken on ToolContext. Per SEP-414, both the legacy
// wire and the stateless wire SEP-2575 use the same `_meta` carrier
// shape, so this accessor returns the same value regardless of the wire
// the request arrived on. Handlers SHOULD NOT branch on the wire.
//
// Callers consume the returned TraceContext as the parent of any spans
// the handler starts (typically by passing ctx — which carries the same
// value — to TracerProvider.StartSpan). Use TraceContext.IsZero to
// detect absence.
//
// Until SEP-414 P2 lands (server middleware that actually extracts
// `_meta.traceparent` on the dispatch path), this accessor returns the
// zero value on every request — the contract is in place so downstream
// code (events EventBus, middleware) can be written and reviewed against
// the eventual wire.
func (bc BaseContext) TraceContext() TraceContext {
	return TraceContextFromContext(bc.Context)
}

// Baggage returns the active W3C Baggage list for this request, or a
// zero Baggage when none has been attached. Symmetric to TraceContext()
// — same dispatch-layer plumbing extracts `_meta.baggage` from the
// inbound request and attaches it via core.WithBaggage before the
// handler runs.
//
// W3C Baggage is a separate W3C standard from W3C Trace Context (they
// can version independently — see SEP-2028 § Predefined Groups), but
// they're commonly propagated together. Use this accessor to read
// arbitrary key=value pairs the upstream caller chose to propagate
// (e.g. tenant_id, user_id, feature flags).
//
// The value is opaque to mcpkit core — the comma-separated W3C list
// format is parsed by adapters (the OTel propagator already implements
// the W3C parsing rules). Use Baggage.IsZero to detect absence.
//
// Stamped onto outbound MCP messages alongside the trace context by
// the server's trace middleware, and onto outbound HTTP calls via
// core.HTTPForwardTransport when handlers compose it into their
// http.Client.
func (bc BaseContext) Baggage() Baggage {
	return BaggageFromContext(bc.Context)
}

// --- BaseContext methods (shared by all handler types) ---

// EmitLog sends a log notification at the given severity level.
//
// Deprecated: per SEP-2577, scheduled for removal in v0.4. See docs/SEP_2577_DEPRECATIONS.md.
func (bc BaseContext) EmitLog(level LogLevel, logger string, data any) {
	if bc.sc == nil || bc.sc.notify == nil || bc.sc.logLevel == nil {
		return
	}
	minLevel := bc.sc.logLevel.Load()
	if minLevel == nil || level < *minLevel {
		return
	}
	bc.sc.notify("notifications/message", LogMessage{
		Level:  level.String(),
		Logger: logger,
		Data:   data,
	})
}

// SessionID returns the transport-assigned session ID for this request.
// Empty for stateless or stdio transports.
func (bc BaseContext) SessionID() string {
	if bc.sc == nil {
		return ""
	}
	return bc.sc.sessionID
}

// Sample sends a sampling/createMessage request to the connected client
// via the legacy server-initiated push path.
//
// Returns ErrNoRequestFunc on the SEP-2575 stateless wire (no per-request
// push channel exists — the spec forbids independent JSON-RPC requests
// on a tools/call response stream). Stateless handlers must enqueue a
// sampling request via MRTR instead:
//
//	return ctx.RequestInput(core.InputRequests{
//	    "draft-summary": core.NewSamplingInputRequest(req),
//	})
//
// The client retries the same tools/call; the handler reads the answer
// via ctx.InputResponse("draft-summary") + core.DecodeSamplingInputResponse.
//
// Deprecated: per SEP-2577, scheduled for removal in v0.4. See docs/SEP_2577_DEPRECATIONS.md.
func (bc BaseContext) Sample(req CreateMessageRequest) (CreateMessageResult, error) {
	if bc.sc == nil || bc.sc.request == nil {
		return CreateMessageResult{}, ErrNoRequestFunc
	}
	if bc.sc.clientCaps == nil || bc.sc.clientCaps.Sampling == nil {
		return CreateMessageResult{}, ErrSamplingNotSupported
	}
	raw, err := bc.sc.request(bc.Context, "sampling/createMessage", req)
	if err != nil {
		return CreateMessageResult{}, err
	}
	var result CreateMessageResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return CreateMessageResult{}, err
	}
	return result, nil
}

// Elicit sends an elicitation/create request to the connected client
// via the legacy server-initiated push path.
//
// SEP-2356: if the client did not declare the `fileInputs` capability,
// the `x-mcp-file` keyword is stripped from `req.RequestedSchema` before
// the request goes on the wire (spec mandate for cap-less clients —
// matches the `tools/list` strip on the server-side dispatch path).
//
// Returns ErrNoRequestFunc on the SEP-2575 stateless wire (server-initiated
// push is forbidden on tools/call streams). Stateless handlers route
// elicitation through MRTR:
//
//	return ctx.RequestInput(core.InputRequests{
//	    "user-name": core.NewElicitationInputRequest(req),
//	})
//
// Caller is responsible for any SEP-2356 strip in that path — the MRTR
// helpers do not introspect ctx for the cap declaration.
func (bc BaseContext) Elicit(req ElicitationRequest) (ElicitationResult, error) {
	if bc.sc == nil || bc.sc.request == nil {
		return ElicitationResult{}, ErrNoRequestFunc
	}
	if bc.sc.clientCaps == nil || bc.sc.clientCaps.Elicitation == nil {
		return ElicitationResult{}, ErrElicitationNotSupported
	}
	if bc.sc.clientCaps.FileInputs == nil {
		req.RequestedSchema = stripFileInputKeywordsRaw(req.RequestedSchema)
	}
	raw, err := bc.sc.request(bc.Context, "elicitation/create", req)
	if err != nil {
		return ElicitationResult{}, err
	}
	var result ElicitationResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return ElicitationResult{}, err
	}
	return result, nil
}

// stripFileInputKeywordsRaw decodes a json.RawMessage schema, strips
// every `x-mcp-file` keyword, and re-encodes. Returns the input
// unchanged when decoding fails or the result has no file-input
// keywords (so well-formed cap-aware schemas don't pay the round-trip
// cost in the no-strip case).
func stripFileInputKeywordsRaw(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		// Couldn't decode — leave unchanged. Server didn't write it as
		// an object schema; nothing to strip.
		return raw
	}
	stripped := stripFileInputKeywordsMap(schema)
	out, err := json.Marshal(stripped)
	if err != nil {
		return raw
	}
	return out
}

// Notify sends an arbitrary server-to-client JSON-RPC notification.
// Returns false if no notification sender is available.
func (bc BaseContext) Notify(method string, params any) bool {
	if bc.sc == nil || bc.sc.notify == nil {
		return false
	}
	bc.sc.notify(method, params)
	return true
}

// AuthClaims returns the authenticated identity, or nil if unavailable.
func (bc BaseContext) AuthClaims() *Claims {
	if bc.sc == nil {
		return nil
	}
	return bc.sc.claims
}

// HasScope checks if the authenticated claims include the given scope.
func (bc BaseContext) HasScope(scope string) bool {
	claims := bc.AuthClaims()
	if claims == nil {
		return false
	}
	for _, s := range claims.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// ClientSupportsExtension checks whether the client declared support for
// the given extension ID during initialize.
func (bc BaseContext) ClientSupportsExtension(extensionID string) bool {
	if bc.sc == nil || bc.sc.clientCaps == nil {
		return false
	}
	_, ok := bc.sc.clientCaps.Extensions[extensionID]
	return ok
}

// ClientSupportsUI checks whether the client declared MCP Apps support.
func (bc BaseContext) ClientSupportsUI() bool {
	return bc.ClientSupportsExtension(UIExtensionID)
}

// NotifyResourcesChanged sends a notifications/resources/list_changed
// to the current session.
func (bc BaseContext) NotifyResourcesChanged() {
	bc.Notify("notifications/resources/list_changed", nil)
}

// NotifyResourceUpdated sends a notifications/resources/updated to all
// sessions subscribed to the given URI.
func (bc BaseContext) NotifyResourceUpdated(uri string) {
	if bc.sc == nil || bc.sc.notifyResourceUpdated == nil {
		return
	}
	bc.sc.notifyResourceUpdated(uri)
}

// IsPathAllowed reports whether the given file path falls within the
// session's enforced roots.
//
// Deprecated: per SEP-2577, scheduled for removal in v0.4. See docs/SEP_2577_DEPRECATIONS.md.
func (bc BaseContext) IsPathAllowed(path string) bool {
	return IsPathAllowed(bc.Context, path)
}

// AllowedRoots returns the current enforced roots for the session.
//
// Deprecated: per SEP-2577, scheduled for removal in v0.4. See docs/SEP_2577_DEPRECATIONS.md.
func (bc BaseContext) AllowedRoots() []string {
	return AllowedRoots(bc.Context)
}

// DetachFromClient returns a BaseContext that preserves session state but is
// NOT cancelled when the client disconnects. See core.DetachFromClient.
func (bc BaseContext) DetachFromClient() BaseContext {
	return BaseContext{context.WithoutCancel(bc.Context), bc.sc}
}

// EmitSSERetry emits an SSE "retry:" hint to the connected client.
func (bc BaseContext) EmitSSERetry(retryAfter time.Duration) error {
	return EmitSSERetry(bc.Context, retryAfter)
}

// DetachFromClient returns a ToolContext that preserves session state but is
// NOT cancelled when the client disconnects.
func (tc ToolContext) DetachFromClient() ToolContext {
	return ToolContext{
		BaseContext:    tc.BaseContext.DetachFromClient(),
		progressToken:  tc.progressToken,
		inputResponses: tc.inputResponses,
		requestState:   tc.requestState,
	}
}

// DetachFromClient returns a ResourceContext that preserves session state but is
// NOT cancelled when the client disconnects.
func (rc ResourceContext) DetachFromClient() ResourceContext {
	return ResourceContext{rc.BaseContext.DetachFromClient()}
}

// DetachFromClient returns a PromptContext that preserves session state but is
// NOT cancelled when the client disconnects.
func (pc PromptContext) DetachFromClient() PromptContext {
	return PromptContext{
		BaseContext:    pc.BaseContext.DetachFromClient(),
		inputResponses: pc.inputResponses,
		requestState:   pc.requestState,
	}
}

// --- ToolContext-only methods ---

// Progress sends a notifications/progress using the stored progress token
// from _meta.progressToken. No-op if the client didn't request progress.
// This is the preferred method in TypedTool handlers where the token is
// automatically captured from the request.
func (tc ToolContext) Progress(progress, total float64, message string) {
	tc.EmitProgress(tc.progressToken, progress, total, message)
}

// ProgressToken returns the stored progress token, or nil if not set.
func (tc ToolContext) ProgressToken() any {
	return tc.progressToken
}

// EmitProgress sends a notifications/progress to the connected client.
// No-op if token is nil. Prefer ctx.Progress() when the token is stored
// in the context (TypedTool handlers, or dispatch with NewToolContextWithProgress).
func (tc ToolContext) EmitProgress(token any, progress, total float64, message string) {
	if token == nil {
		return
	}
	tc.Notify("notifications/progress", ProgressNotification{
		ProgressToken: token,
		Progress:      progress,
		Total:         total,
		Message:       message,
	})
}

// EmitContent sends a partial content block to the client during tool
// execution. On non-streaming transports, the notification is silently dropped.
func (tc ToolContext) EmitContent(requestID json.RawMessage, content Content) {
	if tc.sc == nil || tc.sc.notify == nil {
		return
	}
	method := ContentChunkMethodFromContext(tc.Context)
	tc.sc.notify(method, ContentChunk{
		RequestID: requestID,
		Content:   content,
	})
}

// --- SEP-2322 MRTR (Multi Round-Trip Requests) accessors ---

// InputResponses returns the SEP-2322 inputResponses map the client echoed
// back into this tools/call. Nil on the first call (no prior InputRequiredResult)
// or when the client did not include the field.
//
// Handlers branch on this to detect retries: nil = first call, ask for input;
// non-nil = client has answered, build the final result.
func (tc ToolContext) InputResponses() InputResponses {
	return tc.inputResponses
}

// InputResponse returns the raw response payload for a specific request key
// from the inputResponses map, or nil if the key is missing. The payload
// shape matches the method declared in the original InputRequest
// (ElicitResult, CreateMessageResult, ListRootsResult, ...) — callers
// json.Unmarshal it into the matching typed struct.
func (tc ToolContext) InputResponse(key string) json.RawMessage {
	if tc.inputResponses == nil {
		return nil
	}
	return tc.inputResponses[key]
}

// HasInputResponses reports whether the client echoed any inputResponses
// back. Equivalent to len(ctx.InputResponses()) > 0; provided for readable
// branching in handlers ("if ctx.HasInputResponses() { ... }").
func (tc ToolContext) HasInputResponses() bool {
	return len(tc.inputResponses) > 0
}

// RequestState returns the SEP-2322 requestState token the client echoed
// back. Empty on the first call. The token has already been verified by
// the dispatch layer (HMAC-checked when a signing key is configured),
// so handlers can trust its presence as proof the round-trip is intact —
// the embedded payload itself is opaque to handlers.
func (tc ToolContext) RequestState() string {
	return tc.requestState
}

// RequestInput is the SEP-2322 ephemeral retry primitive. Handlers return
// the value as their ToolResponse to signal "I need more input from the
// client before I can produce a final result"; the dispatch layer mints a
// fresh requestState onto the returned InputRequiredResult before emitting
// it on the wire.
//
// Usage in a tool handler:
//
//	func myTool(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
//	    if !ctx.HasInputResponses() {
//	        return ctx.RequestInput(core.InputRequests{
//	            "user_name": {
//	                Method: "elicitation/create",
//	                Params: rawElicitationParams,
//	            },
//	        })
//	    }
//	    // Decode ctx.InputResponse("user_name") and build the final result.
//	}
//
// The error return is always nil — the helper exists so the call site
// reads as a single return statement matching the ToolHandler signature.
// The concrete return type is InputRequiredResult (not ToolResponse) so
// callers can use the typed value directly when they need to.
func (tc ToolContext) RequestInput(reqs InputRequests) (InputRequiredResult, error) {
	return InputRequiredResult{
		InputRequests: reqs,
	}, nil
}

// --- SEP-2322 MRTR accessors on PromptContext ---
//
// Symmetric with the ToolContext accessors above. SEP-2322 input-required
// flows are scoped to "any request method whose response shape can accept
// it" — prompts/get is the second such method after tools/call (see the
// upstream input-required-result-non-tool-request scenario).

// InputResponses returns the SEP-2322 inputResponses map the client echoed
// back into this prompts/get. Nil on the first call.
func (pc PromptContext) InputResponses() InputResponses {
	return pc.inputResponses
}

// InputResponse returns the raw response payload for a specific request key
// from the inputResponses map, or nil if the key is missing.
func (pc PromptContext) InputResponse(key string) json.RawMessage {
	if pc.inputResponses == nil {
		return nil
	}
	return pc.inputResponses[key]
}

// HasInputResponses reports whether the client echoed any inputResponses back.
func (pc PromptContext) HasInputResponses() bool {
	return len(pc.inputResponses) > 0
}

// RequestState returns the SEP-2322 requestState token the client echoed
// back. Empty on the first call. Already verified by dispatch when a
// signing key is configured.
func (pc PromptContext) RequestState() string {
	return pc.requestState
}

// RequestInput is the SEP-2322 ephemeral retry primitive for prompts/get.
// Symmetric with ToolContext.RequestInput; the dispatch layer reshapes the
// InputRequiredResult onto the wire with a freshly-minted requestState.
func (pc PromptContext) RequestInput(reqs InputRequests) (InputRequiredResult, error) {
	return InputRequiredResult{
		InputRequests: reqs,
	}, nil
}
