package client_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	core "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/testutil"
)

func registerListChangedTestTool(srv *server.Server, name string) {
	srv.RegisterTool(
		core.ToolDef{Name: name, Description: "registered at runtime", InputSchema: map[string]any{"type": "object"}},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			return core.TextResult("late"), nil
		},
	)
}

func waitForListChanged(t *testing.T, got *atomic.Int32, wake <-chan struct{}, want int32, register func(attempt int)) {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(3 * time.Second)
	defer timeout.Stop()

	for attempt := 0; got.Load()&want != want; {
		// Connect opens GET SSE asynchronously. Retry a runtime registration
		// until its notification is observed instead of assuming the response
		// headers mean the server has finished wiring the notification stream.
		register(attempt)
		attempt++
		for got.Load()&want != want {
			select {
			case <-wake:
			case <-ticker.C:
				goto retry
			case <-timeout.C:
				t.Fatal("timed out waiting for notifications/tools/list_changed")
			}
		}
		return
	retry:
	}
}

// TestWithToolsListChangedHandler_DynamicRegistration proves the full chain:
// runtime RegisterTool -> server broadcast -> dedicated client handler, with
// the generic notification callback still receiving the same notification
// (composability is the point of the dedicated option).
func TestWithToolsListChangedHandler_DynamicRegistration(t *testing.T) {
	srv := testutil.NewTestServer()
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)

	var listChangedCallbacks atomic.Int32
	listChangedWake := make(chan struct{}, 1)
	markCallback := func(bit int32) {
		listChangedCallbacks.Or(bit)
		select {
		case listChangedWake <- struct{}{}:
		default:
		}
	}
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "lc-test", Version: "1.0"},
		client.WithGetSSEStream(),
		client.WithToolsListChangedHandler(func() { markCallback(1) }),
		client.WithNotificationCallback(func(method string, _ any) {
			if method == "notifications/tools/list_changed" {
				markCallback(2)
			}
		}),
	)
	if err := c.Connect(t.Context()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	waitForListChanged(t, &listChangedCallbacks, listChangedWake, 3, func(attempt int) {
		registerListChangedTestTool(srv, fmt.Sprintf("late_arrival_%d", attempt))
	})
}

// TestWithoutToolsListChangedHandler_NoFire is the red half: without the
// option nothing invokes the dedicated path (guards against accidental
// wiring through some other channel).
func TestWithoutToolsListChangedHandler_NoFire(t *testing.T) {
	srv := testutil.NewTestServer()
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)

	var listChangedCallbacks atomic.Int32
	listChangedWake := make(chan struct{}, 1)
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "lc-test2", Version: "1.0"},
		client.WithGetSSEStream(),
		client.WithNotificationCallback(func(method string, _ any) {
			if method == "notifications/tools/list_changed" {
				listChangedCallbacks.Or(2)
				select {
				case listChangedWake <- struct{}{}:
				default:
				}
			}
		}),
	)
	if err := c.Connect(t.Context()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	waitForListChanged(t, &listChangedCallbacks, listChangedWake, 2, func(attempt int) {
		registerListChangedTestTool(srv, fmt.Sprintf("late_%d", attempt))
	})
}

func TestWaitForTaskWithInputRequiresHandler(t *testing.T) {
	if _, err := client.WaitForTaskWithInput(context.Background(), nil, "t-1", nil); err == nil {
		t.Fatal("nil handler must be rejected before any network use")
	}
}
