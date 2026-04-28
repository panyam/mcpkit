package ui_test

// Integration tests for ServerRegistry with real MCP servers. Each test
// creates multiple servers with different tool sets and verifies
// aggregation, routing, and collision resolution end-to-end.

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	ui "github.com/panyam/mcpkit/ext/ui"
	"github.com/panyam/mcpkit/server"
)

// newTestServer creates a minimal MCP server with the given tools.
func newTestServer(tools map[string]string) *server.Server {
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	for name, desc := range tools {
		n, d := name, desc // capture
		srv.RegisterTool(
			core.ToolDef{Name: n, Description: d, InputSchema: map[string]any{"type": "object"}},
			func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
				return core.TextResult(n + ":ok"), nil
			},
		)
	}
	return srv
}

// connectInProcess creates a client connected to a server via InProcessTransport.
func connectInProcess(t *testing.T, srv *server.Server) *client.Client {
	t.Helper()
	xport := server.NewInProcessTransport(srv)
	c := client.NewClient("memory://", core.ClientInfo{Name: "test", Version: "1.0"},
		client.WithTransport(xport),
	)
	if err := c.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// TestRegistry_EndToEnd wires two real servers with different tools and
// verifies AllTools aggregation and CallTool routing.
func TestRegistry_EndToEnd(t *testing.T) {
	// Server 1: weather tools
	weatherSrv := newTestServer(map[string]string{
		"get_forecast": "Get weather forecast",
		"get_alerts":   "Get weather alerts",
	})
	weatherClient := connectInProcess(t, weatherSrv)

	// Server 2: calendar tools
	calendarSrv := newTestServer(map[string]string{
		"list_events":  "List calendar events",
		"create_event": "Create calendar event",
	})
	calendarClient := connectInProcess(t, calendarSrv)

	// Create registry and add both servers.
	reg := ui.NewServerRegistry()
	if err := reg.Add(context.Background(), "weather", weatherClient); err != nil {
		t.Fatalf("add weather: %v", err)
	}
	if err := reg.Add(context.Background(), "calendar", calendarClient); err != nil {
		t.Fatalf("add calendar: %v", err)
	}
	defer reg.Close()

	// === Verify AllTools aggregation ===
	t.Run("all tools aggregated", func(t *testing.T) {
		tools, err := reg.AllTools(context.Background())
		if err != nil {
			t.Fatal(err)
		}

		names := map[string]string{}
		for _, rt := range tools {
			names[rt.Name] = rt.ServerID
		}

		for _, expected := range []struct{ name, server string }{
			{"get_forecast", "weather"},
			{"get_alerts", "weather"},
			{"list_events", "calendar"},
			{"create_event", "calendar"},
		} {
			if names[expected.name] != expected.server {
				t.Errorf("%s serverID = %q, want %q", expected.name, names[expected.name], expected.server)
			}
		}
	})

	// === Verify CallTool routing ===
	t.Run("routes to correct server", func(t *testing.T) {
		result, err := reg.CallTool(context.Background(), "get_forecast", nil)
		if err != nil {
			t.Fatal(err)
		}
		if result.Content[0].Text != "get_forecast:ok" {
			t.Errorf("unexpected result: %s", result.Content[0].Text)
		}

		result, err = reg.CallTool(context.Background(), "list_events", nil)
		if err != nil {
			t.Fatal(err)
		}
		if result.Content[0].Text != "list_events:ok" {
			t.Errorf("unexpected result: %s", result.Content[0].Text)
		}
	})

	// === Verify CallToolOn explicit routing ===
	t.Run("explicit routing", func(t *testing.T) {
		result, err := reg.CallToolOn(context.Background(), "weather", "get_forecast", nil)
		if err != nil {
			t.Fatal(err)
		}
		if result.Content[0].Text != "get_forecast:ok" {
			t.Errorf("unexpected result: %s", result.Content[0].Text)
		}
	})

	// === Verify Servers list ===
	t.Run("servers list", func(t *testing.T) {
		ids := reg.Servers()
		if len(ids) != 2 {
			t.Fatalf("expected 2 servers, got %d", len(ids))
		}
		if ids[0] != "calendar" || ids[1] != "weather" {
			t.Errorf("expected [calendar weather], got %v", ids)
		}
	})

	// === Verify Remove drops tools ===
	t.Run("remove drops tools", func(t *testing.T) {
		if err := reg.Remove("weather"); err != nil {
			t.Fatal(err)
		}

		tools, _ := reg.AllTools(context.Background())
		for _, rt := range tools {
			if rt.ServerID == "weather" {
				t.Errorf("weather tool %q still present after remove", rt.Name)
			}
		}

		_, err := reg.CallTool(context.Background(), "get_forecast", nil)
		if err == nil {
			t.Error("expected error calling removed server's tool")
		}
	})
}

// TestRegistry_CollisionResolution_EndToEnd wires two servers with the same
// tool name and verifies that the resolver is invoked.
func TestRegistry_CollisionResolution_EndToEnd(t *testing.T) {
	// Both servers have "get_time".
	srv1 := newTestServer(map[string]string{"get_time": "UTC time"})
	srv2 := newTestServer(map[string]string{"get_time": "Local time"})
	c1 := connectInProcess(t, srv1)
	c2 := connectInProcess(t, srv2)

	var resolverCalled atomic.Int32

	reg := ui.NewServerRegistry(
		ui.WithToolResolver(func(ctx context.Context, name string, candidates []ui.RegisteredTool, args map[string]any) (string, error) {
			resolverCalled.Add(1)
			// Pick based on args.
			if tz, ok := args["timezone"]; ok && tz == "local" {
				return "local-clock", nil
			}
			return "utc-clock", nil
		}),
		ui.WithCollisionHandler(func(name string, ids []string) {
			// Collision detected — we expect "get_time" with 2 servers.
			if name != "get_time" {
				t.Errorf("unexpected collision tool: %s", name)
			}
		}),
	)

	if err := reg.Add(context.Background(), "utc-clock", c1); err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(context.Background(), "local-clock", c2); err != nil {
		t.Fatal(err)
	}
	defer reg.Close()

	// === Verify ambiguity detected ===
	t.Run("collision detected", func(t *testing.T) {
		tools, _ := reg.AllTools(context.Background())
		count := 0
		for _, rt := range tools {
			if rt.Name == "get_time" {
				count++
			}
		}
		if count != 2 {
			t.Errorf("expected 2 get_time entries, got %d", count)
		}
	})

	// === Verify resolver invoked ===
	t.Run("resolver routes correctly", func(t *testing.T) {
		result, err := reg.CallTool(context.Background(), "get_time", map[string]any{"timezone": "local"})
		if err != nil {
			t.Fatal(err)
		}
		if result.Content[0].Text != "get_time:ok" {
			t.Errorf("unexpected result: %s", result.Content[0].Text)
		}
		if resolverCalled.Load() != 1 {
			t.Errorf("resolver called %d times, want 1", resolverCalled.Load())
		}
	})

	// === Verify CallToolOn bypasses resolver ===
	t.Run("explicit routing bypasses resolver", func(t *testing.T) {
		before := resolverCalled.Load()
		result, err := reg.CallToolOn(context.Background(), "utc-clock", "get_time", nil)
		if err != nil {
			t.Fatal(err)
		}
		if result.Content[0].Text != "get_time:ok" {
			t.Errorf("unexpected result: %s", result.Content[0].Text)
		}
		if resolverCalled.Load() != before {
			t.Error("resolver should not be called for CallToolOn")
		}
	})
}

// TestRegistry_WithAppBridge_EndToEnd wires a server with an app bridge and
// verifies that both server and app tools are aggregated and routable.
func TestRegistry_WithAppBridge_EndToEnd(t *testing.T) {
	srv := newTestServer(map[string]string{"server_tool": "A server tool"})
	c := connectInProcess(t, srv)

	bridge := ui.NewInProcessAppBridge()
	bridge.RegisterTool("app_tool", core.ToolDef{Description: "An app tool"}, func(args map[string]any) (any, error) {
		return core.ToolResult{Content: []core.Content{{Type: "text", Text: "app:ok"}}}, nil
	})

	reg := ui.NewServerRegistry()
	if err := reg.AddWithBridge(context.Background(), "hybrid", c, bridge); err != nil {
		t.Fatal(err)
	}
	defer reg.Close()

	// === Verify both sources present ===
	t.Run("aggregates server and app tools", func(t *testing.T) {
		tools, _ := reg.AllTools(context.Background())
		sources := map[string]string{}
		for _, rt := range tools {
			sources[rt.Name] = rt.Source
		}
		if sources["server_tool"] != "server" {
			t.Errorf("server_tool source = %q, want server", sources["server_tool"])
		}
		if sources["app_tool"] != "app" {
			t.Errorf("app_tool source = %q, want app", sources["app_tool"])
		}
	})

	// === Verify routing to app tool ===
	t.Run("routes app tool to bridge", func(t *testing.T) {
		result, err := reg.CallTool(context.Background(), "app_tool", nil)
		if err != nil {
			t.Fatal(err)
		}
		if result.Content[0].Text != "app:ok" {
			t.Errorf("unexpected result: %s", result.Content[0].Text)
		}
	})

	// === Verify routing to server tool ===
	t.Run("routes server tool to client", func(t *testing.T) {
		result, err := reg.CallTool(context.Background(), "server_tool", nil)
		if err != nil {
			t.Fatal(err)
		}
		if result.Content[0].Text != "server_tool:ok" {
			t.Errorf("unexpected result: %s", result.Content[0].Text)
		}
	})
}

// suppress unused import
var _ = strings.Contains
