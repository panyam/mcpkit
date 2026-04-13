package core

import (
	"context"
	"encoding/json"
	"sync/atomic"
)

// NotifyFunc sends a server-to-client JSON-RPC notification.
// method is the notification method (e.g., "notifications/message").
// params will be JSON-marshaled as the notification's params field.
// This type is reusable for all server→client notifications (logging, progress, etc.).
type NotifyFunc func(method string, params any)

// sessionCtx holds per-session state injected into context for tool handlers.
// It carries the transport's notification sender, request sender, the dispatcher's
// log level, client capabilities, and the authenticated claims.
type sessionCtx struct {
	notify     NotifyFunc
	request    RequestFunc               // nil when transport doesn't support server-to-client requests
	logLevel   *atomic.Pointer[LogLevel] // nil pointer in atomic = logging disabled
	clientCaps *ClientCapabilities       // parsed from initialize; nil before handshake
	claims     *Claims                   // nil when no auth or validator doesn't provide claims

	// sseRetry emits a raw SSE "retry:" hint on the session's stream.
	// Non-SSE transports (stdio, in-process, Streamable HTTP JSON path) leave
	// this nil. Set via SetSSERetryHint during session establishment. Reads
	// flow through EmitSSERetry.
	sseRetry func(ms int)

	// allowedRoots returns the current enforced filesystem roots for this
	// session. Computed by the server dispatch layer as the intersection
	// of static WithAllowedRoots and dynamic client roots. Nil = no sandbox.
	// Set via SetAllowedRoots during dispatch.
	allowedRoots func() []string

	// notifyResourceUpdated fans out a notifications/resources/updated to
	// all sessions subscribed to the URI (not just the current session).
	// Wired by dispatchWithOpts from Server.NotifyResourceUpdated. Nil
	// when subscriptions are not enabled. Set via SetNotifyResourceUpdated.
	notifyResourceUpdated func(uri string)
}

type ctxKey int

const sessionCtxKey ctxKey = iota

// ContextWithSession returns a context carrying the session's notification state,
// request sender, client capabilities, and authenticated claims.
// Exported for use by the server sub-package; tool handlers should use
// EmitLog, Sample, Elicit, AuthClaims instead.
func ContextWithSession(ctx context.Context, notify NotifyFunc, request RequestFunc, logLevel *atomic.Pointer[LogLevel], clientCaps *ClientCapabilities, claims *Claims) context.Context {
	return context.WithValue(ctx, sessionCtxKey, &sessionCtx{
		notify:     notify,
		request:    request,
		logLevel:   logLevel,
		clientCaps: clientCaps,
		claims:     claims,
	})
}

// sessionFromContext retrieves the session context, or nil if absent.
func sessionFromContext(ctx context.Context) *sessionCtx {
	sc, _ := ctx.Value(sessionCtxKey).(*sessionCtx)
	return sc
}

// ClientSupportsExtension checks whether the connected client declared support
// for the given extension ID during the initialize handshake. Returns false if
// no session context is present or the client did not advertise the extension.
//
// Usage in a tool handler:
//
//	if core.ClientSupportsExtension(ctx, "io.modelcontextprotocol/ui") {
//	    // client can render MCP Apps
//	}
func ClientSupportsExtension(ctx context.Context, extensionID string) bool {
	sc := sessionFromContext(ctx)
	if sc == nil || sc.clientCaps == nil {
		return false
	}
	_, ok := sc.clientCaps.Extensions[extensionID]
	return ok
}

// Notify sends an arbitrary server-to-client JSON-RPC notification.
// Returns false if no notification sender is available in the context.
// This is the low-level API; prefer EmitLog for logging notifications.
func Notify(ctx context.Context, method string, params any) bool {
	sc := sessionFromContext(ctx)
	if sc == nil || sc.notify == nil {
		return false
	}
	sc.notify(method, params)
	return true
}

// NotifyResourcesChanged sends a notifications/resources/list_changed notification
// to the connected client, signaling that the set of available resources has changed.
// MCP App tool handlers should call this after mutating state that affects the UI
// resource, so clients know to re-fetch resources/list.
//
// Note: this notifies the CURRENT session only (via sc.notify). For targeted
// fan-out to all sessions subscribed to a specific resource URI, use
// NotifyResourceUpdated(ctx, uri) instead.
//
// Safe to call even if no session context is present (no-op).
func NotifyResourcesChanged(ctx context.Context) {
	Notify(ctx, "notifications/resources/list_changed", nil)
}

// FromContext returns a BaseContext for the given context.Context. This is
// the bridge for code using the free-function API — existing free functions
// like EmitLog(ctx, ...) delegate to FromContext(ctx).EmitLog(...).
//
// Always returns a usable BaseContext (never nil). Methods become no-ops
// when no session state is present.
func FromContext(ctx context.Context) BaseContext {
	return BaseContext{ctx, sessionFromContext(ctx)}
}

// MarshalNotification builds a JSON-RPC notification (no id field).
// Exported for use by the server transport sub-package.
func MarshalNotification(method string, params any) (json.RawMessage, error) {
	notification := struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	return json.Marshal(notification)
}
