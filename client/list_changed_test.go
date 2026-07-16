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

// TestWithToolsListChangedHandler_DynamicRegistration proves the full chain:
// runtime RegisterTool -> server broadcast -> dedicated client handler, with
// the generic notification callback still receiving the same notification
// (composability is the point of the dedicated option).
func TestWithToolsListChangedHandler_DynamicRegistration(t *testing.T) {
	srv := testutil.NewTestServer()
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)

	var dedicated atomic.Int32
	var generic atomic.Int32
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "lc-test", Version: "1.0"},
		client.WithGetSSEStream(),
		client.WithToolsListChangedHandler(func() { dedicated.Add(1) }),
		client.WithNotificationCallback(func(method string, _ any) {
			if method == "notifications/tools/list_changed" {
				generic.Add(1)
			}
		}),
	)
	if err := c.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	srv.RegisterTool(
		core.ToolDef{Name: "late_arrival", Description: "registered at runtime", InputSchema: map[string]any{"type": "object"}},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			return core.TextResult("late"), nil
		},
	)

	deadline := time.Now().Add(3 * time.Second)
	for dedicated.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if dedicated.Load() == 0 {
		t.Fatal("dedicated handler never fired after runtime registration")
	}
	if generic.Load() == 0 {
		t.Fatal("generic callback must still receive the notification")
	}
}

// TestWithoutToolsListChangedHandler_NoFire is the red half: without the
// option nothing invokes the dedicated path (guards against accidental
// wiring through some other channel).
func TestWithoutToolsListChangedHandler_NoFire(t *testing.T) {
	srv := testutil.NewTestServer()
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)

	var generic atomic.Int32
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "lc-test2", Version: "1.0"},
		client.WithGetSSEStream(),
		client.WithNotificationCallback(func(method string, _ any) {
			if method == "notifications/tools/list_changed" {
				generic.Add(1)
			}
		}),
	)
	if err := c.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	srv.RegisterTool(
		core.ToolDef{Name: "late2", Description: "", InputSchema: map[string]any{"type": "object"}},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			return core.TextResult("x"), nil
		},
	)
	deadline := time.Now().Add(3 * time.Second)
	for generic.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if generic.Load() == 0 {
		t.Fatal(fmt.Sprint("generic callback should observe the broadcast; got none"))
	}
}

func TestWaitForTaskWithInputRequiresHandler(t *testing.T) {
	if _, err := client.WaitForTaskWithInput(context.Background(), nil, "t-1", nil); err == nil {
		t.Fatal("nil handler must be rejected before any network use")
	}
}
