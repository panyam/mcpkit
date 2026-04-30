package core

import (
	"context"
	"encoding/json"
	"sync"
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
	sessionID  string                    // transport-assigned session ID; empty for stateless/stdio

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

// responseHeaderCollector buffers per-request HTTP response headers set by
// middleware or handlers. Mutex-guarded because background-goroutine middleware
// can race with the transport's read after dispatch returns.
type responseHeaderCollector struct {
	mu      sync.Mutex
	headers map[string]string
}

func (c *responseHeaderCollector) set(k, v string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.headers == nil {
		c.headers = make(map[string]string)
	}
	c.headers[k] = v
}

func (c *responseHeaderCollector) snapshot() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(c.headers))
	for k, v := range c.headers {
		out[k] = v
	}
	return out
}

type responseHeadersCtxKey int

const responseHeadersKey responseHeadersCtxKey = 0

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

// WithResponseHeaderCollector attaches a fresh per-request HTTP response
// header collector to ctx. Transports call this on the inbound request
// context BEFORE dispatching so middleware/handlers can stage headers via
// SetResponseHeader, and the transport reads them via CollectResponseHeaders
// before writing the response. Non-HTTP transports skip this and the
// staging functions become silent no-ops.
func WithResponseHeaderCollector(ctx context.Context) context.Context {
	return context.WithValue(ctx, responseHeadersKey, &responseHeaderCollector{})
}

// SetResponseHeader stages an HTTP response header that the transport applies
// before writing the response body. Used by middleware and handlers that need
// to surface protocol metadata at the HTTP layer (e.g., the v2 task middleware
// emits Mcp-Name: <taskId> per SEP-2243). Silent no-op when the request was
// not wrapped with WithResponseHeaderCollector (e.g., non-HTTP transports).
func SetResponseHeader(ctx context.Context, key, value string) {
	c, _ := ctx.Value(responseHeadersKey).(*responseHeaderCollector)
	if c == nil {
		return
	}
	c.set(key, value)
}

// CollectResponseHeaders returns a snapshot of the response headers staged on
// the current request via SetResponseHeader. HTTP transports call this after
// dispatch returns, before writing response headers. Returns nil when no
// collector was attached or no headers were staged.
func CollectResponseHeaders(ctx context.Context) map[string]string {
	c, _ := ctx.Value(responseHeadersKey).(*responseHeaderCollector)
	if c == nil {
		return nil
	}
	return c.snapshot()
}

// SetSessionID sets the transport-assigned session ID on the session context.
// Called by the server dispatch layer after session creation.
// No-op if no session context is present.
func SetSessionID(ctx context.Context, id string) {
	if sc := sessionFromContext(ctx); sc != nil {
		sc.sessionID = id
	}
}

// GetSessionID returns the session ID from a raw context.Context.
// For use in middleware that doesn't have a typed BaseContext.
// Returns empty string if no session context is present.
func GetSessionID(ctx context.Context) string {
	if sc := sessionFromContext(ctx); sc != nil {
		return sc.sessionID
	}
	return ""
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

// PerRequestClientCapsKey is the SEP-2575 _meta key under which a client may
// declare a per-request capability override. The value is shaped like
// ClientCapabilities and is merged additively on top of the session-level
// capabilities for the duration of one request.
const PerRequestClientCapsKey = "io.modelcontextprotocol/clientCapabilities"

// PerRequestClientCaps decodes the SEP-2575 per-request client-capabilities
// override from raw JSON bytes (typically the value of _meta[PerRequestClientCapsKey]
// in the calling middleware's typed envelope). Returns nil if the bytes are
// empty or malformed — the caller falls back to session-level caps.
func PerRequestClientCaps(raw json.RawMessage) *ClientCapabilities {
	if len(raw) == 0 {
		return nil
	}
	var caps ClientCapabilities
	if err := json.Unmarshal(raw, &caps); err != nil {
		return nil
	}
	return &caps
}

// ClientSupportsExtensionForRequest reports whether the client supports the
// given extension at session level (initialize handshake) OR per-request
// level (SEP-2575 _meta override). Per-request opt-in is additive — it
// cannot revoke a session-level declaration.
//
// requestCapsRaw is the raw JSON bytes from the SEP-2575 _meta override; the
// caller extracts it from a typed envelope (e.g., a json.RawMessage field
// tagged "io.modelcontextprotocol/clientCapabilities" inside _meta). Pass
// an empty slice if the request had no override.
func ClientSupportsExtensionForRequest(ctx context.Context, extensionID string, requestCapsRaw json.RawMessage) bool {
	if ClientSupportsExtension(ctx, extensionID) {
		return true
	}
	caps := PerRequestClientCaps(requestCapsRaw)
	if caps == nil {
		return false
	}
	_, ok := caps.Extensions[extensionID]
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
