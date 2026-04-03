package mcpkit

import "context"

// ProgressNotification is the params payload for a notifications/progress notification.
// Servers send this during long-running operations to report progress to the client.
// The ProgressToken must match the token from the client's original request _meta.
type ProgressNotification struct {
	// ProgressToken is the token from the request's _meta.progressToken field.
	// It links this notification to the original request.
	ProgressToken any `json:"progressToken"`

	// Progress is the current progress value (e.g., bytes processed, items completed).
	Progress float64 `json:"progress"`

	// Total is the expected total value. Zero means indeterminate progress.
	Total float64 `json:"total,omitempty"`

	// Message is an optional human-readable status message.
	Message string `json:"message,omitempty"`
}

// EmitProgress sends a notifications/progress to the connected client.
// Safe to call even if no session context is present or if token is nil (both are no-ops).
//
// token is the ProgressToken from the ToolRequest — pass req.ProgressToken directly.
// progress is the current progress value, total is the expected total (0 for indeterminate).
// message is an optional human-readable status string.
//
// Usage in a tool handler:
//
//	func myHandler(ctx context.Context, req mcpkit.ToolRequest) (mcpkit.ToolResult, error) {
//	    mcpkit.EmitProgress(ctx, req.ProgressToken, 0, 100, "starting")
//	    // ... do work ...
//	    mcpkit.EmitProgress(ctx, req.ProgressToken, 50, 100, "halfway")
//	    // ... more work ...
//	    mcpkit.EmitProgress(ctx, req.ProgressToken, 100, 100, "done")
//	    return mcpkit.TextResult("complete"), nil
//	}
func EmitProgress(ctx context.Context, token any, progress, total float64, message string) {
	if token == nil {
		return
	}
	Notify(ctx, "notifications/progress", ProgressNotification{
		ProgressToken: token,
		Progress:      progress,
		Total:         total,
		Message:       message,
	})
}
