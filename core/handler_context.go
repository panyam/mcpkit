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

// NewToolContext constructs a ToolContext from a standard context.Context.
// Called by the dispatch layer before invoking tool handlers.
func NewToolContext(ctx context.Context) ToolContext {
	return ToolContext{BaseContext{ctx, sessionFromContext(ctx)}}
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
	return ToolContext{tc.BaseContext.DetachFromClient()}
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

// EmitProgress sends a notifications/progress to the connected client.
// No-op if token is nil.
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
