package server_test

import (
	"sync"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNotifyInterceptor_SeesLogNotifications verifies that a NotifyInterceptor
// sees outgoing notifications (e.g., notifications/message from EmitLog) before
// they reach the transport.
func TestNotifyInterceptor_SeesLogNotifications(t *testing.T) {
	var mu sync.Mutex
	var seen []string

	srv := server.NewServer(
		core.ServerInfo{Name: "test", Version: "1.0"},
		server.WithNotifyInterceptor(func(method string, params any, next core.NotifyFunc) {
			mu.Lock()
			seen = append(seen, method)
			mu.Unlock()
			next(method, params) // continue chain
		}),
	)

	// Tool that emits a log notification.
	srv.Register(core.TextTool[struct{}]("log-tool", "Emits a log",
		func(ctx core.ToolContext, _ struct{}) (string, error) {
			ctx.EmitLog(core.LogInfo, "test", "hello")
			return "ok", nil
		},
	))

	testutil.ForAllTransports(t, srv, func(t *testing.T, c *client.Client) {
		mu.Lock()
		seen = nil
		mu.Unlock()

		// Enable logging so EmitLog actually sends notifications.
		require.NoError(t, c.SetLogLevel("debug"))

		_, err := c.ToolCall("log-tool", nil)
		require.NoError(t, err)

		mu.Lock()
		defer mu.Unlock()
		assert.Contains(t, seen, "notifications/message",
			"interceptor should see notifications/message from EmitLog")
	})
}

// TestNotifyInterceptor_Chain verifies that multiple interceptors execute in
// registration order (first = outermost).
func TestNotifyInterceptor_Chain(t *testing.T) {
	var mu sync.Mutex
	var order []string

	srv := server.NewServer(
		core.ServerInfo{Name: "test", Version: "1.0"},
		server.WithNotifyInterceptor(func(method string, params any, next core.NotifyFunc) {
			mu.Lock()
			order = append(order, "A")
			mu.Unlock()
			next(method, params)
		}),
		server.WithNotifyInterceptor(func(method string, params any, next core.NotifyFunc) {
			mu.Lock()
			order = append(order, "B")
			mu.Unlock()
			next(method, params)
		}),
	)

	srv.Register(core.TextTool[struct{}]("log-tool", "Emits a log",
		func(ctx core.ToolContext, _ struct{}) (string, error) {
			ctx.EmitLog(core.LogInfo, "test", "hello")
			return "ok", nil
		},
	))

	// Use in-process transport for deterministic ordering.
	c := client.NewClient("memory://", core.ClientInfo{Name: "test", Version: "1.0"},
		client.WithTransport(server.NewInProcessTransport(srv)),
	)
	require.NoError(t, c.Connect())
	defer c.Close()

	require.NoError(t, c.SetLogLevel("debug"))

	mu.Lock()
	order = nil
	mu.Unlock()

	_, err := c.ToolCall("log-tool", nil)
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	// First registered (A) should execute first (outermost).
	require.GreaterOrEqual(t, len(order), 2)
	assert.Equal(t, "A", order[0], "first interceptor should execute first")
	assert.Equal(t, "B", order[1], "second interceptor should execute second")
}

// TestNotifyInterceptor_Suppress verifies that an interceptor can suppress a
// notification by not calling next.
func TestNotifyInterceptor_Suppress(t *testing.T) {
	var mu sync.Mutex
	var reached bool

	srv := server.NewServer(
		core.ServerInfo{Name: "test", Version: "1.0"},
		// First interceptor suppresses all notifications.
		server.WithNotifyInterceptor(func(method string, params any, next core.NotifyFunc) {
			// Don't call next — notification is suppressed.
		}),
		// Second interceptor should never be reached.
		server.WithNotifyInterceptor(func(method string, params any, next core.NotifyFunc) {
			mu.Lock()
			reached = true
			mu.Unlock()
			next(method, params)
		}),
	)

	srv.Register(core.TextTool[struct{}]("log-tool", "Emits a log",
		func(ctx core.ToolContext, _ struct{}) (string, error) {
			ctx.EmitLog(core.LogInfo, "test", "suppressed")
			return "ok", nil
		},
	))

	c := client.NewClient("memory://", core.ClientInfo{Name: "test", Version: "1.0"},
		client.WithTransport(server.NewInProcessTransport(srv)),
	)
	require.NoError(t, c.Connect())
	defer c.Close()

	require.NoError(t, c.SetLogLevel("debug"))
	_, err := c.ToolCall("log-tool", nil)
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	assert.False(t, reached, "second interceptor should not be reached when first suppresses")
}
