package apps_test

// Conformance tests for ServerRegistry — verifies multi-server tool
// aggregation, routing, collision resolution, and app bridge integration
// with real MCP servers over httptest.

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	ui "github.com/panyam/mcpkit/ext/ui"
	"github.com/panyam/mcpkit/server"
)

// newSimpleServer creates a server with the given tools over httptest.
func newSimpleServer(t *testing.T, tools map[string]string) *client.Client {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	for name, desc := range tools {
		n, d := name, desc
		srv.RegisterTool(
			core.ToolDef{Name: n, Description: d, InputSchema: map[string]any{"type": "object"}},
			func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
				return core.TextResult(n + ":result"), nil
			},
		)
	}
	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "registry-test", Version: "1.0"})
	if err := c.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// TestRegistry_MultiServer_FullStack wires 3 servers over httptest with
// unique and overlapping tools, verifies aggregation, routing, collision
// resolution, and server removal.
func TestRegistry_MultiServer_FullStack(t *testing.T) {
	// Server 1: weather (unique tools + shared "get_info")
	weather := newSimpleServer(t, map[string]string{
		"get_forecast": "Weather forecast",
		"get_info":     "Weather info",
	})

	// Server 2: calendar (unique tools + shared "get_info")
	calendar := newSimpleServer(t, map[string]string{
		"list_events": "Calendar events",
		"get_info":    "Calendar info",
	})

	// Server 3: clock (unique tools only)
	clock := newSimpleServer(t, map[string]string{
		"get_time": "Current time",
	})

	var collisions atomic.Int32
	var resolverCalls atomic.Int32

	reg := ui.NewServerRegistry(
		ui.WithToolResolver(func(ctx context.Context, name string, candidates []ui.RegisteredTool, args map[string]any) (string, error) {
			resolverCalls.Add(1)
			// Route "get_info" based on args.
			if src, ok := args["source"].(string); ok && src == "calendar" {
				return "calendar", nil
			}
			return "weather", nil
		}),
		ui.WithCollisionHandler(func(name string, ids []string) {
			collisions.Add(1)
		}),
	)
	defer reg.Close()

	// Add servers.
	if err := reg.Add(context.Background(), "weather", weather); err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(context.Background(), "calendar", calendar); err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(context.Background(), "clock", clock); err != nil {
		t.Fatal(err)
	}

	// === Collision detected ===
	t.Run("collision detected on add", func(t *testing.T) {
		if collisions.Load() == 0 {
			t.Error("expected collision handler to fire for 'get_info'")
		}
	})

	// === All tools aggregated ===
	t.Run("all tools from 3 servers", func(t *testing.T) {
		tools, err := reg.AllTools(context.Background())
		if err != nil {
			t.Fatal(err)
		}

		names := map[string]int{}
		for _, rt := range tools {
			names[rt.Name]++
		}

		// get_info appears twice (weather + calendar)
		if names["get_info"] != 2 {
			t.Errorf("get_info count = %d, want 2", names["get_info"])
		}
		// Unique tools appear once each
		for _, unique := range []string{"get_forecast", "list_events", "get_time"} {
			if names[unique] != 1 {
				t.Errorf("%s count = %d, want 1", unique, names[unique])
			}
		}
	})

	// === Unambiguous routing ===
	t.Run("routes unique tools directly", func(t *testing.T) {
		result, err := reg.CallTool(context.Background(), "get_forecast", nil)
		if err != nil {
			t.Fatal(err)
		}
		if result.Content[0].Text != "get_forecast:result" {
			t.Errorf("got %q", result.Content[0].Text)
		}
	})

	// === Ambiguous routing with resolver ===
	t.Run("resolver picks server for ambiguous tool", func(t *testing.T) {
		before := resolverCalls.Load()
		_, err := reg.CallTool(context.Background(), "get_info", map[string]any{"source": "calendar"})
		if err != nil {
			t.Fatal(err)
		}
		if resolverCalls.Load() != before+1 {
			t.Error("resolver should have been called")
		}
	})

	// === Explicit routing bypasses resolver ===
	t.Run("CallToolOn bypasses resolver", func(t *testing.T) {
		before := resolverCalls.Load()
		result, err := reg.CallToolOn(context.Background(), "clock", "get_time", nil)
		if err != nil {
			t.Fatal(err)
		}
		if result.Content[0].Text != "get_time:result" {
			t.Errorf("got %q", result.Content[0].Text)
		}
		if resolverCalls.Load() != before {
			t.Error("resolver should not be called for CallToolOn")
		}
	})

	// === Remove server ===
	t.Run("remove drops server tools", func(t *testing.T) {
		if err := reg.Remove("clock"); err != nil {
			t.Fatal(err)
		}
		_, err := reg.CallTool(context.Background(), "get_time", nil)
		if err == nil {
			t.Error("expected error for removed server's tool")
		}

		ids := reg.Servers()
		for _, id := range ids {
			if id == "clock" {
				t.Error("clock should be removed from server list")
			}
		}
	})
}

// suppress unused import
var _ json.RawMessage
