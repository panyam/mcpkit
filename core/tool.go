package core

import (
	"encoding/json"
	"time"
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

	// Execution holds task-related execution metadata.
	// Per MCP spec: declares whether this tool supports async task execution.
	Execution *ToolExecution `json:"execution,omitempty"`

	// Annotations holds optional metadata for this tool.
	// Convention: {"experimental": true} marks experimental tools.
	Annotations map[string]any `json:"annotations,omitempty"`

	// Meta holds protocol-level metadata (e.g., UI presentation hints).
	// Serialized as "_meta" in the tools/list response.
	Meta *ToolMeta `json:"_meta,omitempty"`

	// Timeout is a per-tool execution timeout. If set, overrides the
	// server-wide WithToolTimeout for this tool. Not serialized to clients.
	Timeout time.Duration `json:"-"`

	// RequiredScopes are OAuth scopes the caller's access token must include
	// to invoke this tool. Enforced by ext/auth's scope middleware
	// (auth.NewToolScopeMiddleware), which returns HTTP 403 + WWW-Authenticate
	// when scopes are missing — per SEP-2643 (FineGrainedAuth UC2).
	//
	// Not serialized to clients (it's enforcement metadata, not API contract).
	// Empty/nil means no per-tool scope check; the tool is callable by any
	// authenticated client (subject to global server.WithRequiredScopes).
	RequiredScopes []string `json:"-"`
}

// ToolsListResult is the typed result for tools/list responses.
type ToolsListResult struct {
	Tools      []ToolDef `json:"tools"`
	NextCursor string    `json:"nextCursor,omitempty"`

	// TTL is the SEP-2549 cache freshness hint in SECONDS that the client
	// MAY use to cache the tools list before re-fetching. Semantics mirror
	// HTTP Cache-Control: max-age:
	//
	//   - nil (omitted on the wire) — no server guidance; client falls back
	//     to notifications/list_changed or its own heuristics.
	//   - &0 ("ttl": 0 on the wire) — explicit "do not cache"; client SHOULD
	//     re-fetch every time the list is needed.
	//   - >0 — the list is fresh for this many seconds; the client SHOULD NOT
	//     re-fetch before the TTL expires unless it receives list_changed.
	//
	// Pointer semantics matter: omitempty omits nil but keeps a pointer to 0,
	// so the spec's three states (absent / 0 / positive) round-trip correctly.
	TTL *int `json:"ttl,omitempty"`
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

	// InputResponses is the SEP-2322 ephemeral MRTR retry payload — the
	// client echoes the inputResponses map back into the SAME tools/call
	// request that previously returned an IncompleteResult. Keys MUST match
	// those the server returned in the matching InputRequests; values are
	// opaque per-method response payloads (ElicitResult, CreateMessageResult,
	// ListRootsResult, ...). Nil on the first call.
	//
	// Handlers usually access this through ToolContext.InputResponses() /
	// ToolContext.InputResponse(key); the raw field is here so middleware
	// and conformance tests can inspect the wire payload directly.
	InputResponses InputResponses

	// RequestState is the opaque session-continuation token the client
	// echoed back from a previous IncompleteResult. The dispatch layer
	// verifies it (HMAC when a key is configured) before invoking the
	// handler. Empty on the first call.
	RequestState string
}

// ToolResult is the response from a tool handler.
type ToolResult struct {
	// ResultType is the SEP-2322 polymorphic-dispatch discriminator. For
	// sync tools/call responses the wire value is "complete" (defaulted by
	// MarshalJSON when this field is empty). Task-creating responses use
	// ResultTypeTask on CreateTaskResult; multi-round results use
	// ResultTypeIncomplete on IncompleteResult instead.
	ResultType ResultType `json:"resultType"`

	// Content is the list of content items to return.
	Content []Content `json:"content"`

	// IsError indicates the tool execution failed (but the JSON-RPC call itself succeeded).
	IsError bool `json:"isError,omitempty"`

	// StructuredContent holds optional structured data for the tool result.
	// When the tool has an OutputSchema, this field carries typed data matching
	// that schema. On error (IsError=true), it can carry structured error details.
	// Per MCP spec: "If outputSchema is present, structuredContent SHOULD be included."
	StructuredContent any `json:"structuredContent,omitempty"`

	// Meta holds optional result metadata (e.g., pagination cursor).
	Meta *ToolResultMeta `json:"_meta,omitempty"`

	// IsIncomplete is the in-process sentinel signalling that the handler
	// is returning an MRTR IncompleteResult rather than a final tool result.
	// Set by ctx.RequestInput; the dispatch layer detects it, mints / refreshes
	// the requestState, and reshapes the wire payload as IncompleteResult.
	// Never serialized — this field is in-process plumbing only.
	IsIncomplete bool `json:"-"`

	// InputRequests is the SEP-2322 inputRequests map staged by ctx.RequestInput
	// when IsIncomplete is true. Dispatch reads it to build the IncompleteResult
	// envelope. Nil for normal complete results. Never serialized through
	// ToolResult — dispatch reshapes the response into IncompleteResult.
	InputRequests InputRequests `json:"-"`
}

// MarshalJSON ensures every ToolResult on the wire carries a ResultType.
// Empty defaults to ResultTypeComplete so existing callers and struct
// literals don't have to set the field explicitly. SEP-2322 requires this
// discriminator on every non-task tools/call response so clients can
// dispatch sync vs task vs multi-round without inspecting payload shape.
func (r ToolResult) MarshalJSON() ([]byte, error) {
	type alias ToolResult
	if r.ResultType == "" {
		r.ResultType = ResultTypeComplete
	}
	return json.Marshal(alias(r))
}

// UnmarshalJSON decodes a ToolResult, tolerating a single-object `content`
// form from peers that haven't caught up to the array-form spec. Single
// objects are wrapped into a 1-element slice. See #81.
func (r *ToolResult) UnmarshalJSON(data []byte) error {
	var aux struct {
		ResultType        ResultType      `json:"resultType,omitempty"`
		Content           json.RawMessage `json:"content"`
		IsError           bool            `json:"isError,omitempty"`
		StructuredContent any             `json:"structuredContent,omitempty"`
		Meta              *ToolResultMeta `json:"_meta,omitempty"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	r.ResultType = aux.ResultType
	r.IsError = aux.IsError
	r.StructuredContent = aux.StructuredContent
	r.Meta = aux.Meta
	r.Content = nil
	return decodeContentSlice(aux.Content, &r.Content)
}

// RelatedTaskMeta identifies a task associated with a result. Per MCP spec,
// tasks/result responses MUST include this in _meta["io.modelcontextprotocol/related-task"].
type RelatedTaskMeta struct {
	TaskID string `json:"taskId"`
}

// ToolResultMeta carries optional metadata on a tool result.
type ToolResultMeta struct {
	// NextCursor is a pagination cursor for fetching the next page.
	// Empty when there are no more pages.
	NextCursor string `json:"nextCursor,omitempty"`

	// RelatedTask identifies a task associated with this result.
	// Per MCP spec: set by tasks/result responses.
	RelatedTask *RelatedTaskMeta `json:"io.modelcontextprotocol/related-task,omitempty"`
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

// UnmarshalJSON decodes a Content, tolerating an array-form `resource` field
// from peers that confuse EmbeddedResource (single) with ReadResourceResult
// (array). The first array element wins. See #81 for cardinality rationale.
func (c *Content) UnmarshalJSON(data []byte) error {
	// Use an alias that still has `resource` typed loosely so a sibling decode
	// can route it through the cardinality helper.
	type scalarFields struct {
		Type     string          `json:"type"`
		Text     string          `json:"text,omitempty"`
		MimeType string          `json:"mimeType,omitempty"`
		Data     string          `json:"data,omitempty"`
		Resource json.RawMessage `json:"resource,omitempty"`
	}
	var aux scalarFields
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	c.Type = aux.Type
	c.Text = aux.Text
	c.MimeType = aux.MimeType
	c.Data = aux.Data
	c.Resource = nil
	return decodeResourceContentSingle(aux.Resource, &c.Resource)
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
// The ToolContext provides typed access to session capabilities (EmitLog,
// EmitProgress, EmitContent, Sample, Elicit, etc.) with IDE discoverability.
type ToolHandler func(ctx ToolContext, req ToolRequest) (ToolResult, error)
