package core

import (
	"context"
	"encoding/json"
	"errors"
)

// Sentinel errors for sampling.
var ErrSamplingNotSupported = errors.New("client does not support sampling")

// SamplingMessage is a single message in a sampling/createMessage request.
type SamplingMessage struct {
	Role    string  `json:"role"`
	Content Content `json:"content"`
}

// UnmarshalJSON decodes a SamplingMessage, tolerating an array-form `content`
// field (first element wins). The spec currently specifies single-content for
// SamplingMessage; widening to multi-part is tracked by #141 and will reuse
// this hook by switching to decodeContentSlice. See #81.
func (m *SamplingMessage) UnmarshalJSON(data []byte) error {
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

// ModelHint provides hints about which model the server prefers for sampling.
type ModelHint struct {
	Name string `json:"name,omitempty"`
}

// ModelPreferences describes the server's preferences for model selection
// when the client performs LLM sampling.
type ModelPreferences struct {
	Hints                []ModelHint `json:"hints,omitempty"`
	CostPriority         *float64   `json:"costPriority,omitempty"`
	SpeedPriority        *float64   `json:"speedPriority,omitempty"`
	IntelligencePriority *float64   `json:"intelligencePriority,omitempty"`
}

// SamplingMeta holds protocol-level metadata for a sampling request.
// Serialized as "_meta" in the sampling/createMessage params.
type SamplingMeta struct {
	// UI contains MCP Apps presentation metadata.
	// When set, the host can associate the sampling request with a UI resource.
	UI *UIMetadata `json:"ui,omitempty"`
}

// CreateMessageRequest is the params for a sampling/createMessage server-to-client request.
// The server sends this to ask the client to perform LLM inference.
type CreateMessageRequest struct {
	Messages         []SamplingMessage `json:"messages"`
	SystemPrompt     string            `json:"systemPrompt,omitempty"`
	IncludeContext   string            `json:"includeContext,omitempty"` // "none", "thisServer", "allServers"
	Temperature      *float64          `json:"temperature,omitempty"`
	MaxTokens        int               `json:"maxTokens"`
	ModelPreferences *ModelPreferences `json:"modelPreferences,omitempty"`
	StopSequences    []string          `json:"stopSequences,omitempty"`
	Metadata         map[string]any    `json:"metadata,omitempty"`
	Meta             *SamplingMeta     `json:"_meta,omitempty"`
}

// CreateMessageResult is the client's response to a sampling/createMessage request.
type CreateMessageResult struct {
	Model      string  `json:"model"`
	StopReason string  `json:"stopReason,omitempty"`
	Role       string  `json:"role"`
	Content    Content `json:"content"`
}

// UnmarshalJSON decodes a CreateMessageResult, tolerating an array-form
// `content` field (first element wins) from peers that emit the array shape.
// See #81.
func (r *CreateMessageResult) UnmarshalJSON(data []byte) error {
	var aux struct {
		Model      string          `json:"model"`
		StopReason string          `json:"stopReason,omitempty"`
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	r.Model = aux.Model
	r.StopReason = aux.StopReason
	r.Role = aux.Role
	r.Content = Content{}
	return decodeContentSingle(aux.Content, &r.Content)
}

// Sample sends a sampling/createMessage request to the connected client and blocks
// until the client responds with an LLM inference result.
//
// Returns ErrNoRequestFunc if called outside a session context (e.g., no transport).
// Returns ErrSamplingNotSupported if the client did not declare sampling capability.
// Returns context.DeadlineExceeded if the context expires before the client responds.
//
// Usage in a tool handler:
//
//	func myHandler(ctx context.Context, req mcpkit.ToolRequest) (mcpkit.ToolResult, error) {
//	    result, err := mcpkit.Sample(ctx, mcpkit.CreateMessageRequest{
//	        Messages:  []mcpkit.SamplingMessage{{Role: "user", Content: mcpkit.Content{Type: "text", Text: "summarize this"}}},
//	        MaxTokens: 1000,
//	    })
//	    if err != nil {
//	        return mcpkit.ErrorResult(err.Error()), nil
//	    }
//	    return mcpkit.TextResult(result.Content.Text), nil
//	}
func Sample(ctx context.Context, req CreateMessageRequest) (CreateMessageResult, error) {
	sc := sessionFromContext(ctx)
	if sc == nil || sc.request == nil {
		return CreateMessageResult{}, ErrNoRequestFunc
	}
	if sc.clientCaps == nil || sc.clientCaps.Sampling == nil {
		return CreateMessageResult{}, ErrSamplingNotSupported
	}

	raw, err := sc.request(ctx, "sampling/createMessage", req)
	if err != nil {
		return CreateMessageResult{}, err
	}

	var result CreateMessageResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return CreateMessageResult{}, err
	}
	return result, nil
}
