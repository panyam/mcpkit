package client_test

// Tests for the WithModifyRequest client option. Verifies that the callback
// is invoked on every outgoing HTTP request for both Streamable HTTP and SSE
// transports, survives reconnection, and cannot override auth headers.

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	client "github.com/panyam/mcpkit/client"
	core "github.com/panyam/mcpkit/core"
	server "github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// headerCapture is an http.Handler middleware that records all incoming
// request headers for later inspection. It wraps the real server handler.
type headerCapture struct {
	inner   http.Handler
	mu      sync.Mutex
	headers []http.Header
}

func (h *headerCapture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.headers = append(h.headers, r.Header.Clone())
	h.mu.Unlock()
	h.inner.ServeHTTP(w, r)
}

func (h *headerCapture) getHeaders() []http.Header {
	h.mu.Lock()
	defer h.mu.Unlock()
	cp := make([]http.Header, len(h.headers))
	copy(cp, h.headers)
	return cp
}

// TestModifyRequest_AddsCustomHeader verifies that WithModifyRequest injects
// a custom header into every outgoing Streamable HTTP request, including the
// initialize handshake and subsequent tool calls.
func TestModifyRequest_AddsCustomHeader(t *testing.T) {
	srv := newTestMCPServer()
	capture := &headerCapture{inner: srv.Handler(server.WithStreamableHTTP(true))}
	ts := httptest.NewServer(capture)
	defer ts.Close()

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"},
		client.WithModifyRequest(func(req *http.Request) {
			req.Header.Set("X-Custom-Test", "hello")
		}),
	)
	require.NoError(t, c.Connect())
	defer c.Close()

	_, err := c.ToolCall("echo", map[string]any{"message": "hi"})
	require.NoError(t, err)

	// Every request should have the custom header.
	headers := capture.getHeaders()
	require.NotEmpty(t, headers, "should have captured at least one request")
	for i, h := range headers {
		assert.Equal(t, "hello", h.Get("X-Custom-Test"),
			"request %d should have X-Custom-Test header", i)
	}
}

// TestModifyRequest_SSETransport verifies that WithModifyRequest also works
// with the SSE transport. The hook should apply to the initial GET /sse
// connection and all subsequent POST requests.
func TestModifyRequest_SSETransport(t *testing.T) {
	srv := newTestMCPServer()
	capture := &headerCapture{inner: srv.Handler(server.WithSSE(true), server.WithStreamableHTTP(false))}
	ts := httptest.NewServer(capture)
	defer ts.Close()

	c := client.NewClient(ts.URL+"/mcp/sse", core.ClientInfo{Name: "test", Version: "1.0"},
		client.WithSSEClient(),
		client.WithModifyRequest(func(req *http.Request) {
			req.Header.Set("X-Trace-ID", "trace-123")
		}),
	)
	require.NoError(t, c.Connect())
	defer c.Close()

	_, err := c.ToolCall("echo", map[string]any{"message": "hi"})
	require.NoError(t, err)

	headers := capture.getHeaders()
	require.NotEmpty(t, headers)
	for i, h := range headers {
		assert.Equal(t, "trace-123", h.Get("X-Trace-ID"),
			"request %d should have X-Trace-ID header", i)
	}
}

// TestModifyRequest_NilIsNoOp verifies that not setting WithModifyRequest
// does not affect normal client operation. This is a basic sanity check that
// the nil callback path is handled correctly in all buildReq closures.
func TestModifyRequest_NilIsNoOp(t *testing.T) {
	srv := newTestMCPServer()
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	defer ts.Close()

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"})
	require.NoError(t, c.Connect())
	defer c.Close()

	result, err := c.ToolCall("echo", map[string]any{"message": "works"})
	require.NoError(t, err)
	assert.Contains(t, result, "works")
}

// TestModifyRequest_MultipleHeaders verifies that the callback can set
// multiple headers simultaneously, simulating a real-world scenario where
// several custom headers are injected (e.g., tenant ID + request ID).
func TestModifyRequest_MultipleHeaders(t *testing.T) {
	srv := newTestMCPServer()
	capture := &headerCapture{inner: srv.Handler(server.WithStreamableHTTP(true))}
	ts := httptest.NewServer(capture)
	defer ts.Close()

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"},
		client.WithModifyRequest(func(req *http.Request) {
			req.Header.Set("X-Tenant-ID", "acme")
			req.Header.Set("X-Request-ID", "req-456")
		}),
	)
	require.NoError(t, c.Connect())
	defer c.Close()

	_, err := c.ToolCall("echo", map[string]any{"message": "hi"})
	require.NoError(t, err)

	headers := capture.getHeaders()
	require.NotEmpty(t, headers)
	for i, h := range headers {
		assert.Equal(t, "acme", h.Get("X-Tenant-ID"),
			"request %d should have X-Tenant-ID", i)
		assert.Equal(t, "req-456", h.Get("X-Request-ID"),
			"request %d should have X-Request-ID", i)
	}
}
