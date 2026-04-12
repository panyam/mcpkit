package core

import "context"

// DetachFromClient returns a context that preserves all session state
// (EmitLog, EmitSSERetry, Sample, Elicit, AuthClaims, etc.) but is NOT
// cancelled when the client's HTTP request context is cancelled. Use this
// in a tool handler that needs to continue processing after the client
// disconnects — e.g., long-running computations where the result is
// delivered via EventStore replay on reconnection.
//
// Under the hood, this calls context.WithoutCancel (Go 1.21+), which
// copies all context Values but strips the parent's Done channel and any
// inherited deadlines/timeouts.
//
// Important caveats:
//
//   - Any per-tool timeout (ToolDef.Timeout, WithToolTimeout) set by the
//     server is inherited BEFORE the handler runs. DetachFromClient strips
//     it. If the handler needs a deadline, add one explicitly:
//     ctx = core.DetachFromClient(ctx)
//     ctx, cancel := context.WithTimeout(ctx, 1*time.Hour)
//     defer cancel()
//
//   - Session idle timeouts (WithSessionTimeout) may reap the session
//     while the detached tool is still running if the client doesn't
//     reconnect within the idle window. Combine with a generous
//     WithSSEGracePeriod to keep the session alive long enough.
//
//   - Notifications (EmitLog, EmitSSERetry) emitted after the client
//     disconnects are buffered in the EventStore (if configured) and
//     replayed on reconnection. Without an EventStore, they are dropped.
//
//   - The tool's final ToolResult is still serialized as an SSE event via
//     emitSSEEvent — if the client has disconnected but the session is
//     alive (grace period), the event lands in the store and is replayed.
//
// Example:
//
//	func longRunningTool(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
//	    // Hint the client to reconnect in 5 minutes
//	    core.EmitSSERetry(ctx, 5*time.Minute)
//
//	    // Detach: tool keeps running even if the client drops the connection
//	    ctx = core.DetachFromClient(ctx)
//
//	    // Long computation — ctx.Done() no longer fires on client disconnect
//	    result := doExpensiveWork(ctx)
//
//	    return core.TextResult(result), nil
//	}
func DetachFromClient(ctx context.Context) context.Context {
	return context.WithoutCancel(ctx)
}
