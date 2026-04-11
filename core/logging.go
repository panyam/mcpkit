package core

import (
	"context"
	"encoding/json"
	"sync/atomic"
)

// LogLevel represents MCP log severity levels (syslog-based, ascending severity).
// Used with logging/setLevel to control the minimum level of log notifications
// sent to the client, and with EmitLog to specify the severity of a message.
type LogLevel int

const (
	LogDebug     LogLevel = iota // debug: detailed debugging information
	LogInfo                      // info: general informational messages
	LogNotice                    // notice: normal but significant events
	LogWarning                   // warning: warning conditions
	LogError                     // error: error conditions
	LogCritical                  // critical: critical conditions
	LogAlert                     // alert: action must be taken immediately
	LogEmergency                 // emergency: system is unusable
)

var logLevelNames = map[string]LogLevel{
	"debug":     LogDebug,
	"info":      LogInfo,
	"notice":    LogNotice,
	"warning":   LogWarning,
	"error":     LogError,
	"critical":  LogCritical,
	"alert":     LogAlert,
	"emergency": LogEmergency,
}

var logLevelStrings = [...]string{
	"debug", "info", "notice", "warning", "error", "critical", "alert", "emergency",
}

// String returns the MCP wire name for the log level.
func (l LogLevel) String() string {
	if l >= LogDebug && int(l) < len(logLevelStrings) {
		return logLevelStrings[l]
	}
	return "debug"
}

// ParseLogLevel converts a string to a LogLevel.
// Returns the level and true on success, or (LogDebug, false) for unknown strings.
func ParseLogLevel(s string) (LogLevel, bool) {
	l, ok := logLevelNames[s]
	return l, ok
}

// LogMessage is the params payload for a notifications/message notification.
type LogMessage struct {
	Level  string `json:"level"`
	Logger string `json:"logger,omitempty"`
	Data   any    `json:"data"`
}

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
	request    RequestFunc                // nil when transport doesn't support server-to-client requests
	logLevel   *atomic.Pointer[LogLevel]  // nil pointer in atomic = logging disabled
	clientCaps *ClientCapabilities        // parsed from initialize; nil before handshake
	claims     *Claims                    // nil when no auth or validator doesn't provide claims

	// sseRetry emits a raw SSE "retry:" hint on the session's stream.
	// Non-SSE transports (stdio, in-process, Streamable HTTP JSON path) leave
	// this nil. Set via SetSSERetryHint during session establishment. Reads
	// flow through EmitSSERetry.
	sseRetry func(ms int)
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

// EmitLog sends a notifications/message to the connected client if the session's
// log level allows it. Safe to call even if no session context is present (no-op).
//
// level is the severity of the message. logger is an optional logger name
// (typically the tool or subsystem name). data is the log payload (string, map, etc.).
//
// Usage in a tool handler:
//
//	func myHandler(ctx context.Context, req mcpkit.ToolRequest) (mcpkit.ToolResult, error) {
//	    mcpkit.EmitLog(ctx, mcpkit.LogInfo, "my-tool", "processing started")
//	    // ... do work ...
//	    return mcpkit.TextResult("done"), nil
//	}
func EmitLog(ctx context.Context, level LogLevel, logger string, data any) {
	sc := sessionFromContext(ctx)
	if sc == nil || sc.notify == nil || sc.logLevel == nil {
		return
	}
	minLevel := sc.logLevel.Load()
	if minLevel == nil {
		// Logging not enabled for this session (client never called logging/setLevel)
		return
	}
	if level < *minLevel {
		return
	}
	sc.notify("notifications/message", LogMessage{
		Level:  level.String(),
		Logger: logger,
		Data:   data,
	})
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
// Safe to call even if no session context is present (no-op).
//
// Usage in a tool handler:
//
//	func myHandler(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
//	    // ... mutate state ...
//	    core.NotifyResourcesChanged(ctx)
//	    return core.TextResult("done"), nil
//	}
func NotifyResourcesChanged(ctx context.Context) {
	Notify(ctx, "notifications/resources/list_changed", nil)
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
