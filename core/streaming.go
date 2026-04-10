package core

import (
	"context"
	"encoding/json"
)

// DefaultContentChunkMethod is the default notification method for streaming
// tool content. This is an mcpkit extension — not yet standardized in MCP spec.
// Override per-server via server.WithContentChunkMethod(method).
const DefaultContentChunkMethod = "notifications/tools/content_chunk"

// ContentChunk is the notification payload for a streaming content block.
// Sent during tool execution to deliver partial results incrementally.
type ContentChunk struct {
	// RequestID links the chunk to the original tools/call request.
	RequestID json.RawMessage `json:"requestId"`

	// Content is the partial content block.
	Content Content `json:"content"`
}

// contentChunkMethodKey is the context key for overriding the content chunk
// notification method name.
type contentChunkMethodKey struct{}

// WithContentChunkMethod returns a context with a custom notification method
// for streaming content chunks. Used by the server to plumb the configured
// method name through to EmitContent calls in tool handlers.
func WithContentChunkMethod(ctx context.Context, method string) context.Context {
	return context.WithValue(ctx, contentChunkMethodKey{}, method)
}

// ContentChunkMethodFromContext returns the configured content chunk method,
// or DefaultContentChunkMethod if not set.
func ContentChunkMethodFromContext(ctx context.Context) string {
	if m, ok := ctx.Value(contentChunkMethodKey{}).(string); ok && m != "" {
		return m
	}
	return DefaultContentChunkMethod
}

// EmitContent sends a partial content block to the client during tool
// execution. Each call emits a notification via the session's notify
// function, delivered as an SSE event on streaming transports.
//
// On non-streaming transports (JSON response path), the notification is
// silently dropped — the final ToolResult is the only response.
//
// The tool's final ToolResult should contain the complete aggregated
// content. Streaming chunks are a preview for responsive UX; the final
// result is authoritative.
//
// Example:
//
//	func myTool(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
//	    core.EmitContent(ctx, req.RequestID, core.Content{Type: "text", Text: "Step 1..."})
//	    // ... work ...
//	    core.EmitContent(ctx, req.RequestID, core.Content{Type: "text", Text: "Step 2..."})
//	    return core.TextResult("Complete"), nil
//	}
func EmitContent(ctx context.Context, requestID json.RawMessage, content Content) {
	sc := sessionFromContext(ctx)
	if sc == nil || sc.notify == nil {
		return
	}
	method := ContentChunkMethodFromContext(ctx)
	sc.notify(method, ContentChunk{
		RequestID: requestID,
		Content:   content,
	})
}
