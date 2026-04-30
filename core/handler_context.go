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
	// tools/call request when retrying after a previous IncompleteResult.
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
// CompletionHandler functions. It embeds BaseContext with no additional
// methods.
type PromptContext struct {
	BaseContext
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
	return PromptContext{BaseContext{ctx, sessionFromContext(ctx)}}
}

// --- BaseContext methods (shared by all handler types) ---

// EmitLog sends a log notification at the given severity level.
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

// Sample sends a sampling/createMessage request to the connected client.
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

// Elicit sends an elicitation/create request to the connected client.
func (bc BaseContext) Elicit(req ElicitationRequest) (ElicitationResult, error) {
	if bc.sc == nil || bc.sc.request == nil {
		return ElicitationResult{}, ErrNoRequestFunc
	}
	if bc.sc.clientCaps == nil || bc.sc.clientCaps.Elicitation == nil {
		return ElicitationResult{}, ErrElicitationNotSupported
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
func (bc BaseContext) IsPathAllowed(path string) bool {
	return IsPathAllowed(bc.Context, path)
}

// AllowedRoots returns the current enforced roots for the session.
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
	return PromptContext{pc.BaseContext.DetachFromClient()}
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
// back into this tools/call. Nil on the first call (no prior IncompleteResult)
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
// the value as their ToolResult to signal "I need more input from the
// client before I can produce a final result"; the dispatch layer
// reshapes the response on the wire as an IncompleteResult and mints a
// fresh requestState for the next round.
//
// Usage in a tool handler:
//
//	func myTool(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
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
func (tc ToolContext) RequestInput(reqs InputRequests) (ToolResult, error) {
	return ToolResult{
		IsIncomplete:  true,
		InputRequests: reqs,
	}, nil
}
