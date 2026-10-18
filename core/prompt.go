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

	// Icons are visual identifiers a host may show beside the prompt.
	Icons []Icon `json:"icons,omitempty"`

	// Meta is the prompt's _meta object, passed through verbatim.
	Meta map[string]any `json:"_meta,omitempty"`

	// Timeout is a per-prompt execution timeout. Not serialized to clients.
	Timeout time.Duration `json:"-"`

	// ScopeChallenge decides per request whether the caller needs more
	// authorization before this prompt may be fetched. Nil means no scope gate.
	ScopeChallenge ScopeChallengeFunc `json:"-"`
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
	Name string `json:"name"`
	// Title is an optional display name, preferred over Name in a UI.
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`

	// Schema is an optional JSON Schema for this argument's value, such as a
	// map[string]any with "type", "enum" or "minimum". It is server-side
	// config: the dispatcher validates prompts/get arguments against it and
	// returns -32602 Invalid Params with a structured errors list on failure
	// (#184). Arguments without a Schema bypass validation, and
	// server.WithSchemaValidation(false) turns validation off.
	//
	// It is not sent to clients. No MCP schema defines a schema on
	// PromptArgument, so prompts/list omits it (#1562).
	Schema any `json:"-"`
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
// implementations. [PromptResult] is the sync wire envelope;
// [InputRequiredResult] is the SEP-2322 multi-round variant (issue #452).
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
