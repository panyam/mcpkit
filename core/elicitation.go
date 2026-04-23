package core

import (
	"context"
	"encoding/json"
	"errors"
)

// Elicitation mode constants (SEP-1036).
const (
	// ElicitModeForm is the default form-based elicitation mode.
	// The server sends a JSON schema and the client renders a form.
	ElicitModeForm = "form"

	// ElicitModeURL is the URL-based elicitation mode (SEP-1036).
	// The server directs the user to a URL for out-of-band interaction.
	// The MCP client's bearer token remains unchanged.
	ElicitModeURL = "url"
)

// Sentinel errors for elicitation.
var (
	ErrElicitationNotSupported    = errors.New("client does not support elicitation")
	ErrElicitationURLNotSupported = errors.New("client does not support URL-mode elicitation")
)

// ElicitationMeta holds protocol-level metadata for an elicitation request.
// Serialized as "_meta" in the elicitation/create params.
type ElicitationMeta struct {
	// UI contains MCP Apps presentation metadata.
	// When set, the host can render a UI resource during input collection
	// instead of falling back to the default schema-driven form.
	UI *UIMetadata `json:"ui,omitempty"`

	// RelatedTask identifies the task this request is associated with.
	// Set automatically by TaskContext.TaskElicit() so the client can
	// correlate side-channel requests with the originating task.
	RelatedTask *RelatedTaskMeta `json:"io.modelcontextprotocol/related-task,omitempty"`
}

// ElicitationRequest is the params for an elicitation/create server-to-client request.
// The server sends this to ask the client to collect structured user input.
//
// For form mode (default): Message + RequestedSchema are used.
// For URL mode (SEP-1036): Message + URL + ElicitationID are used;
// RequestedSchema must NOT be set.
type ElicitationRequest struct {
	Message         string           `json:"message"`
	RequestedSchema json.RawMessage  `json:"requestedSchema,omitempty"`
	Mode            string           `json:"mode,omitempty"`            // "form" (default) or "url"
	URL             string           `json:"url,omitempty"`             // Required for url mode
	ElicitationID   string           `json:"elicitationId,omitempty"`   // Required for url mode; correlates with completion notification
	Meta            *ElicitationMeta `json:"_meta,omitempty"`
}

// ElicitationResult is the client's response to an elicitation/create request.
type ElicitationResult struct {
	Action  string         `json:"action"` // "accept", "decline", or "cancel"
	Content map[string]any `json:"content,omitempty"`
}

// ElicitationCompleteParams is the params for the notifications/elicitation/complete
// notification (SEP-1036). The server sends this after the user completes an
// out-of-band URL-mode elicitation flow. The client uses elicitationId to
// correlate with the original elicitation request and may retry the denied operation.
type ElicitationCompleteParams struct {
	ElicitationID string `json:"elicitationId"`
}

// Elicit sends a form-mode elicitation/create request to the connected client
// and blocks until the client responds with user input.
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

// ElicitURL sends a URL-mode elicitation/create request to the connected client
// (SEP-1036). The client presents the URL to the user for out-of-band interaction.
// The client's bearer token remains unchanged.
//
// Returns ErrElicitationURLNotSupported if the client did not declare URL-mode
// elicitation capability.
//
// After calling this, the server should send notifications/elicitation/complete
// (via NotifyElicitationComplete) when the out-of-band flow is done.
func ElicitURL(ctx context.Context, req ElicitationRequest) (ElicitationResult, error) {
	sc := sessionFromContext(ctx)
	if sc == nil || sc.request == nil {
		return ElicitationResult{}, ErrNoRequestFunc
	}
	if sc.clientCaps == nil || sc.clientCaps.Elicitation == nil {
		return ElicitationResult{}, ErrElicitationNotSupported
	}
	if sc.clientCaps.Elicitation.URL == nil {
		return ElicitationResult{}, ErrElicitationURLNotSupported
	}

	// Enforce URL-mode fields.
	req.Mode = ElicitModeURL

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

// NotifyElicitationComplete sends a notifications/elicitation/complete notification
// to the client (SEP-1036). Call this after the user completes the out-of-band
// URL-mode elicitation flow so the client knows it can retry the original request.
func NotifyElicitationComplete(ctx context.Context, elicitationID string) bool {
	return Notify(ctx, "notifications/elicitation/complete", ElicitationCompleteParams{
		ElicitationID: elicitationID,
	})
}
