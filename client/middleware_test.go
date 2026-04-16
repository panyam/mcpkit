package client_test

import (
	"context"
	"sync"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClientMiddleware_SeesMethodAndParams verifies that client middleware
// receives the correct method name and params for each outgoing call.
func TestClientMiddleware_SeesMethodAndParams(t *testing.T) {
	var mu sync.Mutex
	var methods []string

	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	srv.Register(core.TextTool[struct{}]("hello", "Says hello",
		func(ctx core.ToolContext, _ struct{}) (string, error) { return "hi", nil },
	))

	c := client.NewClient("memory://", core.ClientInfo{Name: "test", Version: "1.0"},
		client.WithTransport(server.NewInProcessTransport(srv)),
		client.WithClientMiddleware(func(ctx context.Context, method string, params any,
			next client.ClientCallFunc) (*client.CallResult, error) {
			mu.Lock()
			methods = append(methods, method)
			mu.Unlock()
			return next(ctx, method, params)
		}),
	)
	require.NoError(t, c.Connect())
	defer c.Close()

	_, err := c.ToolCall("hello", nil)
	require.NoError(t, err)

	_, err = c.Call("tools/list", nil)
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, methods, "tools/call")
	assert.Contains(t, methods, "tools/list")
}

// TestClientMiddleware_Chain verifies that multiple middleware execute in
// registration order (first registered = outermost).
func TestClientMiddleware_Chain(t *testing.T) {
	var mu sync.Mutex
	var order []string

	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	srv.Register(core.TextTool[struct{}]("ping", "Ping",
		func(ctx core.ToolContext, _ struct{}) (string, error) { return "pong", nil },
	))

	c := client.NewClient("memory://", core.ClientInfo{Name: "test", Version: "1.0"},
		client.WithTransport(server.NewInProcessTransport(srv)),
		client.WithClientMiddleware(
			func(ctx context.Context, method string, params any,
				next client.ClientCallFunc) (*client.CallResult, error) {
				mu.Lock()
				order = append(order, "A-before")
				mu.Unlock()
				result, err := next(ctx, method, params)
				mu.Lock()
				order = append(order, "A-after")
				mu.Unlock()
				return result, err
			},
			func(ctx context.Context, method string, params any,
				next client.ClientCallFunc) (*client.CallResult, error) {
				mu.Lock()
				order = append(order, "B-before")
				mu.Unlock()
				result, err := next(ctx, method, params)
				mu.Lock()
				order = append(order, "B-after")
				mu.Unlock()
				return result, err
			},
		),
	)
	require.NoError(t, c.Connect())
	defer c.Close()

	_, err := c.ToolCall("ping", nil)
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	// A is outermost: A-before → B-before → handler → B-after → A-after
	// But tools/call goes through Call, and Connect also calls initialize via Call.
	// Find the last 4 entries (from the ToolCall).
	n := len(order)
	require.GreaterOrEqual(t, n, 4)
	last4 := order[n-4:]
	assert.Equal(t, []string{"A-before", "B-before", "B-after", "A-after"}, last4)
}

// TestClientMiddleware_ShortCircuit verifies that middleware can return
// without calling next, short-circuiting the call chain.
func TestClientMiddleware_ShortCircuit(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	srv.Register(core.TextTool[struct{}]("ping", "Ping",
		func(ctx core.ToolContext, _ struct{}) (string, error) { return "real", nil },
	))

	var innerCalled bool

	c := client.NewClient("memory://", core.ClientInfo{Name: "test", Version: "1.0"},
		client.WithTransport(server.NewInProcessTransport(srv)),
		client.WithClientMiddleware(
			// First middleware short-circuits tools/call with a cached response.
			func(ctx context.Context, method string, params any,
				next client.ClientCallFunc) (*client.CallResult, error) {
				if method == "tools/call" {
					return &client.CallResult{Raw: []byte(`{"content":[{"type":"text","text":"cached"}]}`)}, nil
				}
				return next(ctx, method, params)
			},
			// Second middleware should not be reached for tools/call.
			func(ctx context.Context, method string, params any,
				next client.ClientCallFunc) (*client.CallResult, error) {
				if method == "tools/call" {
					innerCalled = true
				}
				return next(ctx, method, params)
			},
		),
	)
	require.NoError(t, c.Connect())
	defer c.Close()

	result, err := c.ToolCall("ping", nil)
	require.NoError(t, err)
	assert.Equal(t, "cached", result, "should get cached response from middleware")
	assert.False(t, innerCalled, "inner middleware should not be reached")
}
