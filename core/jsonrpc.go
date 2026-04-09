package core

import "encoding/json"

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
	Result  json.RawMessage `json:"result,omitempty"`
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
)

// NewResponse creates a success response for the given request ID.
func NewResponse(id json.RawMessage, result any) *Response {
	raw, _ := json.Marshal(result)
	return &Response{JSONRPC: "2.0", ID: id, Result: raw}
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
