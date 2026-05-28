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

	// TTLMs is the SEP-2549 cache-freshness hint in integer milliseconds.
	// See ToolsListResult.TTLMs for full semantics — nil/absent and &0 are
	// both "immediately stale", &N>0 is "fresh for N milliseconds".
	TTLMs *int `json:"ttlMs,omitempty"`

	// CacheScope is the SEP-2549 cache-scope hint. See ToolsListResult.CacheScope.
	CacheScope string `json:"cacheScope,omitempty"`
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

	// InputResponses is the SEP-2322 ephemeral MRTR retry payload — the
	// client echoes the inputResponses map back into the SAME prompts/get
	// request that previously returned an InputRequiredResult. Symmetric
	// with ToolRequest.InputResponses. Nil on the first call.
	InputResponses InputResponses

	// RequestState is the opaque session-continuation token the client
	// echoed back from a previous InputRequiredResult. The dispatch layer
	// verifies it (HMAC when a key is configured) before invoking the
	// handler. Empty on the first call.
	RequestState string
}

// PromptResponse is the sealed interface returned by PromptHandler
// implementations. Today only [PromptResult] (the sync wire envelope)
// implements it; a future [InputRequiredResult] PromptResponse impl plugs in
// by adding a one-line promptResponse() method (see issue #452 / SEP-2322
// prompt scenarios).
//
// The interface is sealed via the unexported promptResponse() marker so
// external types cannot impersonate a core response variant.
type PromptResponse interface {
	promptResponse()
}

// PromptResult is the response from a prompt handler.
type PromptResult struct {
	Description string          `json:"description,omitempty"`
	Messages    []PromptMessage `json:"messages"`
}

func (PromptResult) promptResponse() {}

// PromptHandler generates prompt messages, optionally using arguments.
//
// Returns the sealed [PromptResponse] interface — handlers typically return
// a [PromptResult] literal which satisfies the interface.
type PromptHandler func(ctx PromptContext, req PromptRequest) (PromptResponse, error)
