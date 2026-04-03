package mcpkit

import (
	"context"
	"encoding/json"
)

// ToolDef describes a tool exposed via MCP.
type ToolDef struct {
	// Name is the tool identifier used in tools/call.
	Name string `json:"name"`

	// Description is a human-readable summary of what the tool does.
	Description string `json:"description"`

	// InputSchema is the JSON Schema for the tool's arguments.
	// Typically a map[string]any with "type": "object", "properties": {...}, "required": [...].
	InputSchema any `json:"inputSchema"`
}

// ToolRequest is the validated input passed to a ToolHandler.
type ToolRequest struct {
	// Name of the tool being called.
	Name string

	// Arguments is the raw JSON arguments from the tools/call params.
	Arguments json.RawMessage

	// RequestID is the JSON-RPC request ID.
	RequestID json.RawMessage
}

// ToolResult is the response from a tool handler.
type ToolResult struct {
	// Content is the list of content items to return.
	Content []Content `json:"content"`

	// IsError indicates the tool execution failed (but the JSON-RPC call itself succeeded).
	IsError bool `json:"isError,omitempty"`
}

// Content is a single content item in a tool result.
// Supports text, image, audio, and embedded resource types per MCP spec.
type Content struct {
	Type     string           `json:"type"`
	Text     string           `json:"text,omitempty"`
	MimeType string           `json:"mimeType,omitempty"`
	Data     string           `json:"data,omitempty"`
	Resource *ResourceContent `json:"resource,omitempty"`
}

// ResourceContent is an embedded resource reference in a tool result.
type ResourceContent struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}

// TextResult creates a ToolResult with a single text content item.
func TextResult(text string) ToolResult {
	return ToolResult{
		Content: []Content{{Type: "text", Text: text}},
	}
}

// ErrorResult creates a ToolResult marked as an error with the given message.
func ErrorResult(text string) ToolResult {
	return ToolResult{
		Content: []Content{{Type: "text", Text: text}},
		IsError: true,
	}
}

// Bind unmarshals the tool arguments into the provided struct.
func (r *ToolRequest) Bind(v any) error {
	return json.Unmarshal(r.Arguments, v)
}

// ToolHandler is the function signature for tool implementations.
type ToolHandler func(ctx context.Context, req ToolRequest) (ToolResult, error)
