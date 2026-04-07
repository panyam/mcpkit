package core

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
	// Arbitrary JSON Schema fields (e.g. "$schema", "$defs", "$ref",
	// "additionalProperties") are preserved as-is through registration,
	// serialization, and client-side deserialization.
	InputSchema any `json:"inputSchema"`

	// OutputSchema is an optional JSON Schema for the tool's structuredContent output.
	// When present, the tool SHOULD return StructuredContent matching this schema.
	// Per MCP spec: enables clients to validate and process tool output programmatically.
	OutputSchema any `json:"outputSchema,omitempty"`

	// Annotations holds optional metadata for this tool.
	// Convention: {"experimental": true} marks experimental tools.
	Annotations map[string]any `json:"annotations,omitempty"`
}

// ToolRequest is the validated input passed to a ToolHandler.
type ToolRequest struct {
	// Name of the tool being called.
	Name string

	// Arguments is the raw JSON arguments from the tools/call params.
	Arguments json.RawMessage

	// RequestID is the JSON-RPC request ID.
	RequestID json.RawMessage

	// ProgressToken is the token from the request's _meta.progressToken field.
	// Nil if the client did not request progress reporting. Pass this to
	// EmitProgress to send notifications/progress notifications.
	ProgressToken any
}

// ToolResult is the response from a tool handler.
type ToolResult struct {
	// Content is the list of content items to return.
	Content []Content `json:"content"`

	// IsError indicates the tool execution failed (but the JSON-RPC call itself succeeded).
	IsError bool `json:"isError,omitempty"`

	// StructuredContent holds optional structured data for the tool result.
	// When the tool has an OutputSchema, this field carries typed data matching
	// that schema. On error (IsError=true), it can carry structured error details.
	// Per MCP spec: "If outputSchema is present, structuredContent SHOULD be included."
	StructuredContent any `json:"structuredContent,omitempty"`
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

// StructuredResult creates a ToolResult with both text content and structured data.
// Use this when the tool has an OutputSchema — structuredContent carries typed data
// matching the schema, while content provides a human-readable summary.
func StructuredResult(text string, data any) ToolResult {
	return ToolResult{
		Content:           []Content{{Type: "text", Text: text}},
		StructuredContent: data,
	}
}

// StructuredError creates a ToolResult marked as an error with both text and
// structured error data. Use this to return machine-readable error details
// alongside a human-readable error message.
func StructuredError(text string, data any) ToolResult {
	return ToolResult{
		Content:           []Content{{Type: "text", Text: text}},
		IsError:           true,
		StructuredContent: data,
	}
}

// Bind unmarshals the tool arguments into the provided struct.
func (r *ToolRequest) Bind(v any) error {
	return json.Unmarshal(r.Arguments, v)
}

// ToolHandler is the function signature for tool implementations.
type ToolHandler func(ctx context.Context, req ToolRequest) (ToolResult, error)
