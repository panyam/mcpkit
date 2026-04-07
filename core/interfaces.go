package core

import "context"

// Transport is the minimal interface for client-server communication.
// Both HTTP transports and the in-process transport satisfy this interface.
// The client package consumes it; the server package provides implementations.
type Transport interface {
	// Connect establishes the transport (e.g., SSE handshake, session creation).
	Connect(ctx context.Context) error

	// Call sends a JSON-RPC request and returns the response.
	Call(ctx context.Context, req *Request) (*Response, error)

	// Notify sends a JSON-RPC notification (no response expected).
	Notify(ctx context.Context, req *Request) error

	// Close shuts down the transport.
	Close() error

	// SessionID returns the transport's session identifier (empty if none).
	SessionID() string
}

// ServerRequestHandler handles server-to-client JSON-RPC requests (sampling,
// elicitation). The client registers this on the transport so the server can
// send requests during tool execution and receive responses.
type ServerRequestHandler func(ctx context.Context, req *Request) *Response

// NotificationHandler receives server-to-client notifications (logging,
// progress, resource updates). Used by tests to verify notification delivery.
type NotificationHandler func(method string, params []byte)
