package mcpkit

// In-memory transport for the MCP client. Calls Server.Dispatch() directly
// without HTTP, serialization overhead, or network. Useful for:
//   - Unit and integration tests (fast, no port conflicts, race detector works)
//   - Embedded scenarios (client + server in same process)
//   - Benchmarking tool handlers without transport noise

import (
	"context"
	"encoding/json"
	"fmt"
)

// WithInMemoryServer creates a client that talks to the given server
// directly in-memory, bypassing HTTP transport entirely.
//
// Example:
//
//	srv := mcpkit.NewServer(mcpkit.ServerInfo{Name: "test", Version: "1.0"})
//	srv.RegisterTool(def, handler)
//	client := mcpkit.NewClient("memory://", info, mcpkit.WithInMemoryServer(srv))
//	client.Connect()
//	result, _ := client.ToolCall("my-tool", args)
func WithInMemoryServer(srv *Server) ClientOption {
	return func(c *Client) {
		c.useSSE = false
		c.transport = &memoryTransport{server: srv}
	}
}

// memoryTransport implements clientTransport by dispatching directly to a Server.
type memoryTransport struct {
	server     *Server
	dispatcher *Dispatcher
}

// connect creates a per-session dispatcher (same as what HTTP transports do).
func (t *memoryTransport) connect() error {
	t.dispatcher = t.server.newSession()
	return nil
}

// call dispatches a JSON-RPC request and returns the response.
// Marshals/unmarshals to maintain the same data flow as HTTP transports.
func (t *memoryTransport) call(data []byte) (*rpcResponse, error) {
	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("memory transport: invalid request: %w", err)
	}

	resp := t.server.dispatchWith(t.dispatcher, context.Background(), nil, &req)
	if resp == nil {
		return nil, nil
	}

	// Convert Response → rpcResponse (the client's internal type)
	raw, err := json.Marshal(resp)
	if err != nil {
		return nil, fmt.Errorf("memory transport: marshal response: %w", err)
	}
	var rr rpcResponse
	if err := json.Unmarshal(raw, &rr); err != nil {
		return nil, fmt.Errorf("memory transport: unmarshal response: %w", err)
	}
	return &rr, nil
}

// notify dispatches a JSON-RPC notification (no response expected).
func (t *memoryTransport) notify(data []byte) error {
	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		return fmt.Errorf("memory transport: invalid notification: %w", err)
	}
	t.server.dispatchWith(t.dispatcher, context.Background(), nil, &req)
	return nil
}

func (t *memoryTransport) close() error        { return nil }
func (t *memoryTransport) getSessionID() string { return "memory" }
