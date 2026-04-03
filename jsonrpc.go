package mcpkit

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

// Standard JSON-RPC error codes.
const (
	ErrCodeParse          = -32700
	ErrCodeInvalidRequest = -32600
	ErrCodeMethodNotFound = -32601
	ErrCodeInvalidParams  = -32602
	ErrCodeInternal       = -32603
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
