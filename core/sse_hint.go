package core

import (
	"context"
	"time"
)

// SSE reconnection-hint API (#72).
//
// EmitSSERetry lets tool/resource/prompt handlers tell the connected client
// "if you disconnect, reconnect after this long" by emitting an SSE "retry:"
// field on the session's stream. It is the mcpkit-level entry point for the
// servicekit v0.0.23 writer-side retry support.
//
// Use cases:
//   - Long-running tool: "back off for 30s, I'll still be working when you come back"
//   - Load-shed: "reconnect in 5m, we're at capacity"
//   - Maintenance window: "reconnect in 1h"
//
// This is a hint, not a disconnect. The server does not close the stream.
// Clients MAY disconnect and reconnect using their own logic; when they do,
// they SHOULD respect the most recent retry value received. If the client
// stays connected, the hint is purely informational.
//
// Combine with WithSSEGracePeriod + EventStore on the server to support the
// full reconnect-later pattern: the session persists across the disconnect
// and missed events are replayed on reconnection via Last-Event-ID.

// EmitSSERetry emits an SSE "retry:" hint to the connected client on the
// current session's SSE stream. The retryAfter duration is rounded down to
// whole milliseconds and sent to the client as the next reconnection delay.
//
// No-op on non-SSE transports (stdio, in-process, Streamable HTTP responses
// that chose the JSON path over SSE). Returns nil in all cases — callers
// don't need to branch on transport type.
//
// Non-positive durations are dropped (no hint emitted). The servicekit writer
// additionally drops zero/negative retry values to prevent "reconnect
// immediately" thundering herds.
//
// Thread safety: safe to call from any goroutine spawned by the handler.
// Delivery is asynchronous — the function returns as soon as the message is
// enqueued on the SSE hub writer.
//
// Usage in a tool handler:
//
//	func longRunningTool(ctx context.Context, req mcpkit.ToolRequest) (mcpkit.ToolResult, error) {
//	    // Tell the client to back off for 30s before reconnecting if the
//	    // connection drops. The tool continues running regardless.
//	    mcpkit.EmitSSERetry(ctx, 30*time.Second)
//
//	    // ...do long work...
//
//	    return mcpkit.TextResult("done"), nil
//	}
func EmitSSERetry(ctx context.Context, retryAfter time.Duration) error {
	if retryAfter <= 0 {
		return nil
	}
	sc := sessionFromContext(ctx)
	if sc == nil || sc.sseRetry == nil {
		return nil
	}
	ms := int(retryAfter / time.Millisecond)
	if ms <= 0 {
		return nil
	}
	sc.sseRetry(ms)
	return nil
}

// SetSSERetryHint installs a retry-hint emitter on the session stored in ctx.
// Exported for the server transport layer to wire in an SSE-capable emitter
// during session setup. Non-SSE transports omit this call and EmitSSERetry
// becomes a silent no-op for handlers running under that transport.
//
// Must be called AFTER ContextWithSession has populated the session context.
// Returns ctx unchanged if no session context is present (in which case the
// setter has nothing to attach to).
//
// Idempotent: calling twice overwrites the previous emitter. Intended to be
// called exactly once per session during transport setup.
func SetSSERetryHint(ctx context.Context, fn func(ms int)) context.Context {
	if sc := sessionFromContext(ctx); sc != nil {
		sc.sseRetry = fn
	}
	return ctx
}
