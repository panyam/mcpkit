package core

import (
	"context"
	"encoding/json"
	"errors"
)

// Sentinel errors for elicitation.
var ErrElicitationNotSupported = errors.New("client does not support elicitation")

// ElicitationMeta holds protocol-level metadata for an elicitation request.
// Serialized as "_meta" in the elicitation/create params.
type ElicitationMeta struct {
	// UI contains MCP Apps presentation metadata.
	// When set, the host can render a UI resource during input collection
	// instead of falling back to the default schema-driven form.
	UI *UIMetadata `json:"ui,omitempty"`
}

// ElicitationRequest is the params for an elicitation/create server-to-client request.
// The server sends this to ask the client to collect structured user input.
type ElicitationRequest struct {
	Message         string           `json:"message"`
	RequestedSchema json.RawMessage  `json:"requestedSchema,omitempty"`
	Meta            *ElicitationMeta `json:"_meta,omitempty"`
}

// ElicitationResult is the client's response to an elicitation/create request.
type ElicitationResult struct {
	Action  string         `json:"action"` // "accept", "decline", or "cancel"
	Content map[string]any `json:"content,omitempty"`
}

// Elicit sends an elicitation/create request to the connected client and blocks
// until the client responds with user input.
//
// Returns ErrNoRequestFunc if called outside a session context (e.g., no transport).
// Returns ErrElicitationNotSupported if the client did not declare elicitation capability.
// Returns context.DeadlineExceeded if the context expires before the client responds.
//
// Usage in a tool handler:
//
//	func myHandler(ctx context.Context, req mcpkit.ToolRequest) (mcpkit.ToolResult, error) {
//	    result, err := mcpkit.Elicit(ctx, mcpkit.ElicitationRequest{
//	        Message: "Which database should I connect to?",
//	        RequestedSchema: json.RawMessage(`{
//	            "type": "object",
//	            "properties": {"database": {"type": "string", "enum": ["prod", "staging", "dev"]}}
//	        }`),
//	    })
//	    if err != nil {
//	        return mcpkit.ErrorResult(err.Error()), nil
//	    }
//	    if result.Action != "accept" {
//	        return mcpkit.TextResult("User declined"), nil
//	    }
//	    return mcpkit.TextResult(fmt.Sprintf("Selected: %v", result.Content["database"])), nil
//	}
func Elicit(ctx context.Context, req ElicitationRequest) (ElicitationResult, error) {
	sc := sessionFromContext(ctx)
	if sc == nil || sc.request == nil {
		return ElicitationResult{}, ErrNoRequestFunc
	}
	if sc.clientCaps == nil || sc.clientCaps.Elicitation == nil {
		return ElicitationResult{}, ErrElicitationNotSupported
	}

	raw, err := sc.request(ctx, "elicitation/create", req)
	if err != nil {
		return ElicitationResult{}, err
	}

	var result ElicitationResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return ElicitationResult{}, err
	}
	return result, nil
}
