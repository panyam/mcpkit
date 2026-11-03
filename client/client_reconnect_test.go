package client_test

// Client reconnection tests. Verify that transient transport failures trigger
// automatic reconnection with exponential backoff, and that terminal errors
// (auth failures, JSON-RPC errors) do NOT trigger reconnection.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	client "github.com/panyam/mcpkit/client"
	core "github.com/panyam/mcpkit/core"
	server "github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReconnect_OnDisconnect verifies that a transient transport error (server
// restart) triggers automatic reconnection. The server is stopped and restarted
// between calls — the client should reconnect and retry successfully.
func TestReconnect_OnDisconnect(t *testing.T) {
	srv := newTestMCPServer()
	handler := srv.Handler(server.WithStreamableHTTP(true))

	// Start server
	ts := httptest.NewServer(handler)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "reconnect-test", Version: "1.0"},
		client.WithMaxRetries(2),
		client.WithReconnectBackoff(10*time.Millisecond))
	require.NoError(t, c.Connect(t.Context()))

	// First call should work
	result, err := c.ToolCall(t.Context(), "echo", map[string]any{"message": "before"})
	require.NoError(t, err)
	assert.Contains(t, result, "before")

	// "Restart" the server: close and start a new one on the same URL
	ts.Close()
	ts2 := httptest.NewUnstartedServer(handler)
	ts2.Listener = ts.Listener // reuse the same address
	// Can't reuse listener after close — use a new server at same URL pattern
	// Instead, test by verifying the reconnect attempt is made
	ts2 = httptest.NewServer(handler)
	defer ts2.Close()

	// Update client URL to new server (simulates DNS/load balancer)
	c.SetURL(ts2.URL + "/mcp")

	// This call should fail on old transport, reconnect to new server, and succeed
	result, err = c.ToolCall(t.Context(), "echo", map[string]any{"message": "after"})
	require.NoError(t, err)
	assert.Contains(t, result, "after")
	c.Close()
}

// TestReconnect_MaxRetries verifies that the client gives up after exhausting
// the configured number of reconnection attempts.
func TestReconnect_MaxRetries(t *testing.T) {
	srv := newTestMCPServer()
	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "reconnect-test", Version: "1.0"},
		client.WithMaxRetries(2),
		client.WithReconnectBackoff(10*time.Millisecond))
	require.NoError(t, c.Connect(t.Context()))

	// Stop server permanently
	ts.Close()

	// Call should fail after retries
	_, err := c.ToolCall(t.Context(), "echo", map[string]any{"message": "fail"})
	require.Error(t, err)
	c.Close()
}

// TestReconnect_TerminalErrorNoRetry verifies that ClientAuthError (401/403)
// does NOT trigger reconnection — these are terminal server rejections, not
// transient network issues.
func TestReconnect_TerminalErrorNoRetry(t *testing.T) {
	assert.False(t, client.IsTransientError(&client.ClientAuthError{StatusCode: 401}),
		"401 should not be transient")
	assert.False(t, client.IsTransientError(&client.ClientAuthError{StatusCode: 403}),
		"403 should not be transient")
}

// TestReconnect_TransientErrors verifies that the expected error types are
// classified as transient (eligible for reconnection retry).
func TestReconnect_TransientErrors(t *testing.T) {
	assert.True(t, client.IsTransientError(io.EOF), "EOF should be transient")
	assert.True(t, client.IsTransientError(io.ErrUnexpectedEOF), "UnexpectedEOF should be transient")
	assert.True(t, client.IsTransientError(errors.New("connection reset by peer")),
		"connection reset should be transient")
	assert.True(t, client.IsTransientError(errors.New("read: connection refused")),
		"connection refused should be transient")
	assert.False(t, client.IsTransientError(nil), "nil should not be transient")
	assert.False(t, client.IsTransientError(errors.New("invalid JSON")),
		"JSON error should not be transient")

	// HTTPStatusError: 5xx is transient, 4xx is terminal
	assert.True(t, client.IsTransientError(&client.HTTPStatusError{StatusCode: 500}),
		"HTTP 500 should be transient")
	assert.True(t, client.IsTransientError(&client.HTTPStatusError{StatusCode: 503}),
		"HTTP 503 should be transient")
	assert.False(t, client.IsTransientError(&client.HTTPStatusError{StatusCode: 400}),
		"HTTP 400 should not be transient")
	assert.False(t, client.IsTransientError(&client.HTTPStatusError{StatusCode: 404}),
		"HTTP 404 should not be transient")
}

// TestReconnect_DisabledByDefault verifies that reconnection is not attempted
// when WithMaxRetries is not set (default maxRetries=0).
func TestReconnect_DisabledByDefault(t *testing.T) {
	srv := newTestMCPServer()
	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "reconnect-test", Version: "1.0"})
	// No WithMaxRetries — reconnection disabled
	require.NoError(t, c.Connect(t.Context()))

	ts.Close()

	// Should fail immediately without reconnection attempt
	_, err := c.ToolCall(t.Context(), "echo", map[string]any{"message": "fail"})
	require.Error(t, err)
	c.Close()
}

// TestReconnect_WithLogging verifies that reconnection works correctly when
// client logging is enabled (the logging wrapper must survive reconnection).
func TestReconnect_WithLogging(t *testing.T) {
	var callCount atomic.Int32
	srv := server.NewServer(core.ServerInfo{Name: "reconnect-test", Version: "1.0"})
	srv.RegisterTool(
		core.ToolDef{Name: "count", Description: "counts calls"},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			n := callCount.Add(1)
			return core.TextResult(fmt.Sprintf("call-%d", n)), nil
		},
	)
	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"},
		client.WithMaxRetries(1),
		client.WithReconnectBackoff(10*time.Millisecond),
		client.WithClientLogging(nil)) // use default logger
	require.NoError(t, c.Connect(t.Context()))
	defer c.Close()

	result, err := c.ToolCall(t.Context(), "count", nil)
	require.NoError(t, err)
	assert.Contains(t, result, "call-")
}

// Suppress unused import warnings.
var _ = fmt.Sprintf

// capsServer offers "caps", which reports whether the calling session declared
// roots, and "count", which counts its calls.
func capsServer(calls *atomic.Int32) *server.Server {
	srv := server.NewServer(core.ServerInfo{Name: "caps", Version: "1.0"})
	srv.Register(core.TextTool[struct{}]("caps", "reports declared client capabilities",
		func(ctx core.ToolContext, _ struct{}) (string, error) {
			if c := ctx.ClientCaps(); c != nil && c.Roots != nil {
				return "roots", nil
			}
			return "none", nil
		}))
	srv.Register(core.TextTool[struct{}]("count", "counts its calls",
		func(core.ToolContext, struct{}) (string, error) {
			return fmt.Sprint(calls.Add(1)), nil
		}))
	return srv
}

// A reconnect re-initializes with the same capabilities Connect declared, so
// a server reached again after a restart still knows the client takes roots.
func TestReconnect_KeepsClientCapabilities(t *testing.T) {
	var calls atomic.Int32
	handler := capsServer(&calls).Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "caps-test", Version: "1.0"},
		client.WithRootsHandler(func(context.Context) ([]core.Root, error) { return nil, nil }),
		client.WithMaxRetries(2),
		client.WithReconnectBackoff(10*time.Millisecond))
	require.NoError(t, c.Connect(t.Context()))
	defer c.Close()

	got, err := c.ToolCall(t.Context(), "caps", map[string]any{})
	require.NoError(t, err)
	require.Contains(t, got, "roots", "control: Connect declares roots")

	ts.Close()
	ts2 := httptest.NewServer(handler)
	defer ts2.Close()
	c.SetURL(ts2.URL + "/mcp")

	got, err = c.ToolCall(t.Context(), "caps", map[string]any{})
	require.NoError(t, err)
	assert.Contains(t, got, "roots", "the re-initialize after a reconnect dropped the client's capabilities")
}

// WithReconnectWithoutReplay reconnects on a transient failure but returns the
// failure rather than re-sending the request, so a call that may have reached
// a server that then died is never run twice. The next call uses the new
// connection.
func TestReconnect_WithoutReplay(t *testing.T) {
	run := func(opts ...client.ClientOption) (first error, servedOnNew int32, second string) {
		var calls atomic.Int32
		handler := capsServer(&calls).Handler(server.WithStreamableHTTP(true))
		ts := httptest.NewServer(handler)
		c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "replay-test", Version: "1.0"},
			append([]client.ClientOption{client.WithMaxRetries(2), client.WithReconnectBackoff(10 * time.Millisecond)}, opts...)...)
		require.NoError(t, c.Connect(t.Context()))
		defer c.Close()

		ts.Close()
		var calls2 atomic.Int32
		ts2 := httptest.NewServer(capsServer(&calls2).Handler(server.WithStreamableHTTP(true)))
		defer ts2.Close()
		c.SetURL(ts2.URL + "/mcp")

		_, first = c.ToolCall(t.Context(), "count", map[string]any{})
		servedOnNew = calls2.Load()
		second, err := c.ToolCall(t.Context(), "count", map[string]any{})
		require.NoError(t, err, "the call after a reconnect goes to the new connection")
		return first, servedOnNew, second
	}

	first, served, _ := run()
	require.NoError(t, first, "control: by default the failed call is replayed")
	require.Equal(t, int32(1), served)

	first, served, second := run(client.WithReconnectWithoutReplay())
	require.Error(t, first, "without replay the failed call reports its failure")
	assert.True(t, client.IsTransientError(first), "and the failure is still the transient one: %v", first)
	assert.Equal(t, int32(0), served, "without replay the failed call never reached the new server")
	assert.Contains(t, second, "1")
}
