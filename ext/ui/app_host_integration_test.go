package ui_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	ui "github.com/panyam/mcpkit/ext/ui"
	"github.com/panyam/mcpkit/server"
)

// TestAppHost_EndToEnd wires a real MCP server with tools, a real client, and
// an AppHost with InProcessAppBridge. It verifies both directions:
//   - Host→App: calling an app-provided tool through AppHost
//   - App→Host: app calling a server-side tool through the bridge
//   - Aggregation: ListAllTools returns both server and app tools
func TestAppHost_EndToEnd(t *testing.T) {
	// --- Set up MCP server with one tool ---
	srv := server.NewServer(core.ServerInfo{Name: "test-server", Version: "1.0"})
	srv.RegisterTool(
		core.ToolDef{Name: "server_echo", Description: "Server-side echo"},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			var args map[string]string
			json.Unmarshal(req.Arguments, &args)
			return core.TextResult("server:" + args["msg"]), nil
		},
	)

	// --- Set up client connected to server via in-process transport ---
	xport := server.NewInProcessTransport(srv)
	c := client.NewClient("memory://", core.ClientInfo{Name: "test-host", Version: "1.0"},
		client.WithTransport(xport),
		client.WithUIExtension(),
	)
	if err := c.Connect(); err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer c.Close()

	// --- Set up AppBridge with app-provided tools ---
	bridge := ui.NewInProcessAppBridge()
	bridge.RegisterTool("app_greet", core.ToolDef{Description: "App-side greeting"}, func(args map[string]any) (any, error) {
		name, _ := args["name"].(string)
		return core.ToolResult{
			Content: []core.Content{{Type: "text", Text: "hello " + name}},
		}, nil
	})

	// --- Create and start AppHost ---
	host := ui.NewAppHost(c, bridge)
	if err := host.Start(context.Background()); err != nil {
		t.Fatalf("host start: %v", err)
	}
	defer host.Close()

	// === Test 1: Host→App — call app tool through AppHost ===
	t.Run("host calls app tool", func(t *testing.T) {
		result, err := host.CallAppTool(context.Background(), "app_greet", map[string]any{"name": "world"})
		if err != nil {
			t.Fatalf("CallAppTool: %v", err)
		}
		if len(result.Content) == 0 || result.Content[0].Text != "hello world" {
			t.Errorf("unexpected result: %+v", result)
		}
	})

	// === Test 2: App→Host — app calls server tool through bridge ===
	t.Run("app calls server tool", func(t *testing.T) {
		resp, err := bridge.SendToHost(context.Background(), "tools/call", map[string]any{
			"name":      "server_echo",
			"arguments": map[string]any{"msg": "from-app"},
		})
		if err != nil {
			t.Fatalf("SendToHost: %v", err)
		}
		if resp.Error != nil {
			t.Fatalf("server error: %s", resp.Error.Message)
		}
		// The response comes back as CallResult.Raw → json.RawMessage
		// which the bridge wraps into Response.Result.
		raw, err := ui.ToBytes(resp.Result)
		if err != nil {
			t.Fatal(err)
		}
		// The server returns a ToolResult; check it contains the echoed text.
		text := string(raw)
		if !contains(text, "server:from-app") {
			t.Errorf("expected server:from-app in result, got %s", text)
		}
	})

	// === Test 3: ListAllTools — aggregates server + app tools ===
	t.Run("list all tools", func(t *testing.T) {
		tools, err := host.ListAllTools(context.Background())
		if err != nil {
			t.Fatalf("ListAllTools: %v", err)
		}
		names := map[string]bool{}
		for _, td := range tools {
			names[td.Name] = true
		}
		if !names["server_echo"] {
			t.Error("missing server_echo in aggregated tools")
		}
		if !names["app_greet"] {
			t.Error("missing app_greet in aggregated tools")
		}
	})

	// === Test 4: Dynamic tool registration — list_changed refreshes cache ===
	t.Run("dynamic tool registration", func(t *testing.T) {
		bridge.RegisterTool("app_dice", core.ToolDef{Description: "Roll dice"}, func(args map[string]any) (any, error) {
			return core.ToolResult{Content: []core.Content{{Type: "text", Text: "4"}}}, nil
		})

		// Wait for async refresh triggered by notifications/tools/list_changed.
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			tools, _ := host.ListAppTools(context.Background())
			if len(tools) == 2 { // app_greet + app_dice
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("app_dice did not appear in tool list within 2s")
	})
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
