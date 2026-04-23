package core

import (
	"bytes"
	"encoding/json"
)

// JSON-RPC 2.0 types for MCP protocol communication.

// Request is a JSON-RPC 2.0 request envelope.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC 2.0 response.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is a JSON-RPC 2.0 error object.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// IsNotification returns true if this request has no ID (JSON-RPC notification).
func (r *Request) IsNotification() bool {
	return r.ID == nil || string(r.ID) == "null"
}

// PingResult is the typed result for ping responses.
// Currently empty per spec, but typed to allow future extension.
type PingResult struct{}

// Standard JSON-RPC 2.0 error codes (https://www.jsonrpc.org/specification#error_object).
//
// Reserved ranges:
//
//	-32700           Parse error (invalid JSON)
//	-32600           Invalid Request (not a valid JSON-RPC request)
//	-32601           Method not found
//	-32602           Invalid params
//	-32603           Internal error
//	-32000 to -32099 Server error (implementation-defined)
const (
	// Standard JSON-RPC 2.0 error codes — use only for JSON-RPC protocol errors.
	ErrCodeParse          = -32700 // Invalid JSON
	ErrCodeInvalidRequest = -32600 // Not a valid JSON-RPC request
	ErrCodeMethodNotFound = -32601 // Method not found
	ErrCodeInvalidParams  = -32602 // Invalid params
	ErrCodeInternal       = -32603 // Internal JSON-RPC error (marshaling, framework bugs)

	// ErrCodeServerError is the base of the implementation-defined range (-32000 to -32099).
	// Avoid using this directly — prefer the MCP-specific codes below.
	ErrCodeServerError = -32000

	// MCP application error codes — outside the JSON-RPC reserved range.
	// These indicate application-level failures in tool, resource, or prompt handlers.
	ErrCodeToolExecutionError = -31000 // Tool handler returned an error
	ErrCodeResourceError      = -31001 // Resource handler returned an error
	ErrCodePromptError        = -31002 // Prompt handler returned an error
	ErrCodeCompletionError    = -31003 // Completion handler returned an error

	// ErrCodeURLElicitationRequired indicates that the request cannot proceed
	// until the user completes one or more URL-based elicitation flows (SEP-1036).
	// The error data contains an elicitations array with URL-mode elicitation
	// requests. Clients should present the URLs to the user and retry after
	// receiving notifications/elicitation/complete.
	//
	// This error code is also the composition point for FineGrainedAuth (UC1):
	// the authorization denial envelope is additive metadata in the same data
	// object, alongside the elicitations array.
	ErrCodeURLElicitationRequired = -32042
)

// NewResponse creates a success response for the given request ID.
// Result is stored as-is and serialized once when the transport sends it.
func NewResponse(id json.RawMessage, result any) *Response {
	return &Response{JSONRPC: "2.0", ID: id, Result: result}
}

// ResultAs unmarshals the response result into v. Use this when you
// need to inspect the result after it has been through JSON round-trip
// (e.g., in tests or client code). Handles both typed results (any)
// and pre-serialized json.RawMessage.
func (r *Response) ResultAs(v any) error {
	if r.Result == nil {
		return nil
	}
	// If already json.RawMessage, unmarshal directly.
	if raw, ok := r.Result.(json.RawMessage); ok {
		return json.Unmarshal(raw, v)
	}
	// Otherwise marshal then unmarshal (typed result).
	raw, err := MarshalJSON(r.Result)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

// MarshalJSON encodes v as JSON without HTML-escaping <, >, &.
// Go's json.Marshal escapes these to \u003c/\u003e/\u0026 for HTML safety,
// but JSON-RPC payloads are not HTML contexts. This matches how Node.js
// and Python serialize JSON.
func MarshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	b := buf.Bytes()
	if len(b) > 0 && b[len(b)-1] == '\n' {
		b = b[:len(b)-1]
	}
	return b, nil
}

// NewErrorResponse creates an error response for the given request ID.
func NewErrorResponse(id json.RawMessage, code int, message string) *Response {
	return &Response{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &Error{Code: code, Message: message},
	}
}

// NewErrorResponseWithData creates an error response with additional structured data.
// Used for protocol errors that carry machine-readable context (e.g., supported versions).
func NewErrorResponseWithData(id json.RawMessage, code int, message string, data any) *Response {
	return &Response{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &Error{Code: code, Message: message, Data: data},
	}
}

// URLElicitationRequiredErrorData is the structured data for a -32042 error.
// The Elicitations field carries URL-mode elicitation requests. Extra holds
// additional metadata (e.g., authorization denial context from FineGrainedAuth
// UC1). Extra keys are flattened into the top-level JSON object.
type URLElicitationRequiredErrorData struct {
	Elicitations []ElicitationRequest `json:"elicitations"`
	Extra        map[string]any       `json:"-"`
}

// MarshalJSON flattens Extra keys into the top-level alongside elicitations.
func (d URLElicitationRequiredErrorData) MarshalJSON() ([]byte, error) {
	m := make(map[string]any, 1+len(d.Extra))
	m["elicitations"] = d.Elicitations
	for k, v := range d.Extra {
		m[k] = v
	}
	return json.Marshal(m)
}

// NewURLElicitationRequiredError creates a -32042 error response indicating
// that URL-based elicitation flows must be completed before retrying.
func NewURLElicitationRequiredError(id json.RawMessage, message string, data URLElicitationRequiredErrorData) *Response {
	return NewErrorResponseWithData(id, ErrCodeURLElicitationRequired, message, data)
}

// isJSONRPCResponse detects whether raw JSON is a JSON-RPC response (not a request).
// A response has an "id" field and either "result" or "error", but no "method" field.
// Used by transports to route incoming client messages that are responses to
// server-to-client requests (sampling/createMessage, elicitation/create).
func IsJSONRPCResponse(data []byte) bool {
	var probe struct {
		Method string          `json:"method"`
		ID     json.RawMessage `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return false
	}
	// Has ID, no method, and has result or error → it's a response.
	return probe.Method == "" && probe.ID != nil && string(probe.ID) != "null" &&
		(probe.Result != nil || probe.Error != nil)
}
