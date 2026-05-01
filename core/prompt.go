package core

import (
	"encoding/json"
	"time"
)

// PromptDef describes a prompt exposed via MCP.
type PromptDef struct {
	// Name is the prompt identifier used in prompts/get.
	Name string `json:"name"`

	// Title is an optional display title.
	Title string `json:"title,omitempty"`

	// Description explains what this prompt does.
	Description string `json:"description,omitempty"`

	// Arguments defines the parameters this prompt accepts.
	Arguments []PromptArgument `json:"arguments,omitempty"`

	// Annotations holds optional metadata for this prompt.
	Annotations map[string]any `json:"annotations,omitempty"`

	// Timeout is a per-prompt execution timeout. Not serialized to clients.
	Timeout time.Duration `json:"-"`
}

// PromptsListResult is the typed result for prompts/list responses.
type PromptsListResult struct {
	Prompts    []PromptDef `json:"prompts"`
	NextCursor string      `json:"nextCursor,omitempty"`

	// TTL is the SEP-2549 cache freshness hint in seconds. See
	// ToolsListResult.TTL for full semantics — same three-state pointer
	// shape (nil = no guidance, &0 = do not cache, &N>0 = N seconds fresh).
	TTL *int `json:"ttl,omitempty"`
}

// PromptArgument describes a single argument to a prompt.
type PromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`

	// Schema is an optional JSON Schema describing the expected value shape
	// for this argument. Mirrors ToolDef.InputSchema: typically a
	// map[string]any with "type", "enum", "minimum", etc. Arbitrary JSON
	// Schema keywords ($ref, $defs, additionalProperties, ...) are preserved
	// as-is through registration, serialization, and client deserialization.
	//
	// Enforced server-side: when set, the dispatcher validates incoming
	// argument values against the schema before invoking the handler and
	// returns -32602 Invalid Params with a structured errors list on
	// failure (#184). Arguments without a Schema bypass validation.
	// Use server.WithSchemaValidation(false) to opt out of call-time
	// validation if you prefer to validate in the handler.
	Schema any `json:"schema,omitempty"`
}

// PromptMessage is a single message in a prompt result.
type PromptMessage struct {
	Role    string  `json:"role"`
	Content Content `json:"content"` // reuses Content from tool.go
}

// UnmarshalJSON decodes a PromptMessage, tolerating an array-form `content`
// field from peers that emit the array shape by mistake. First element wins.
// See #81.
func (m *PromptMessage) UnmarshalJSON(data []byte) error {
	var aux struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	m.Role = aux.Role
	m.Content = Content{}
	return decodeContentSingle(aux.Content, &m.Content)
}

// PromptRequest is the validated input passed to a PromptHandler.
//
// Arguments holds the decoded JSON values from the prompts/get request, keyed
// by argument name. Values retain their JSON types after decode: strings stay
// as string, numbers become float64, booleans stay bool, objects become
// map[string]any, arrays become []any. Handlers type-assert as needed. This
// shape mirrors how tool handlers receive ToolRequest.Arguments (raw JSON),
// but pre-decoded for ergonomic access — a prompt argument count is tiny, so
// eager decode is fine. See #87.
type PromptRequest struct {
	Name      string
	Arguments map[string]any
}

// PromptResult is the response from a prompt handler.
type PromptResult struct {
	Description string          `json:"description,omitempty"`
	Messages    []PromptMessage `json:"messages"`
}

// PromptHandler generates prompt messages, optionally using arguments.
type PromptHandler func(ctx PromptContext, req PromptRequest) (PromptResult, error)
