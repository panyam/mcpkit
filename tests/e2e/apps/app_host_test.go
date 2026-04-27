package apps_test

// Conformance tests for AppHost — verifies that a host implementation using
// AppHost + InProcessAppBridge can:
//   - List and call app-provided tools (host→app)
//   - Forward app requests to the MCP server (app→host→server)
//   - Aggregate tools from both server and app
//   - React to dynamic tool registration via notifications/tools/list_changed

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	ui "github.com/panyam/mcpkit/ext/ui"
	"github.com/panyam/mcpkit/server"
)

// setupAppHost creates a conformance server, a connected client, an
// InProcessAppBridge with pre-registered app tools, and an AppHost wiring
// them together. Cleanup is registered on t.
func setupAppHost(t *testing.T) (*ui.AppHost, *ui.InProcessAppBridge, *client.Client) {
	t.Helper()

	srv := newConformanceServer()
	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "apphost-conformance", Version: "1.0"},
		client.WithUIExtension(),
	)
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	bridge := ui.NewInProcessAppBridge()
	bridge.RegisterTool("app_counter", core.ToolDef{
		Description: "Increment and return a counter",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"amount": map[string]any{"type": "number"}},
		},
	}, func(args map[string]any) (any, error) {
		amt := 1.0
		if v, ok := args["amount"].(float64); ok {
			amt = v
		}
		return core.ToolResult{
			Content: []core.Content{{Type: "text", Text: jsonNum(amt)}},
		}, nil
	})

	bridge.RegisterTool("app_echo", core.ToolDef{
		Description: "Echo back the input",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"message": map[string]any{"type": "string"}},
		},
	}, func(args map[string]any) (any, error) {
		msg, _ := args["message"].(string)
		return core.ToolResult{
			Content: []core.Content{{Type: "text", Text: "echo: " + msg}},
		}, nil
	})

	host := ui.NewAppHost(c, bridge)
	if err := host.Start(context.Background()); err != nil {
		t.Fatalf("AppHost.Start: %v", err)
	}
	t.Cleanup(func() { host.Close() })

	return host, bridge, c
}

func jsonNum(v float64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// TestAppHost_ListAppTools verifies that the host can enumerate tools
// registered by the app via the bridge.
func TestAppHost_ListAppTools(t *testing.T) {
	host, _, _ := setupAppHost(t)

	tools, err := host.ListAppTools(context.Background())
	if err != nil {
		t.Fatalf("ListAppTools: %v", err)
	}

	names := map[string]bool{}
	for _, td := range tools {
		names[td.Name] = true
	}
	if !names["app_counter"] {
		t.Error("missing app_counter in app tools")
	}
	if !names["app_echo"] {
		t.Error("missing app_echo in app tools")
	}
}

// TestAppHost_CallAppTool verifies the host→app tools/call round-trip:
// AppHost sends tools/call to the bridge, the bridge dispatches to the
// registered handler, and the result comes back as a ToolResult.
func TestAppHost_CallAppTool(t *testing.T) {
	host, _, _ := setupAppHost(t)

	result, err := host.CallAppTool(context.Background(), "app_echo", map[string]any{"message": "conformance"})
	if err != nil {
		t.Fatalf("CallAppTool: %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("expected content in result")
	}
	if result.Content[0].Text != "echo: conformance" {
		t.Errorf("text = %q, want %q", result.Content[0].Text, "echo: conformance")
	}
}

// TestAppHost_AppCallsServerTool verifies the app→host→server round-trip:
// the app calls a server-side tool (show-dashboard) through the bridge,
// which AppHost forwards to the MCP server via Client.Call().
func TestAppHost_AppCallsServerTool(t *testing.T) {
	_, bridge, _ := setupAppHost(t)

	resp, err := bridge.SendToHost(context.Background(), "tools/call", map[string]any{
		"name":      "show-dashboard",
		"arguments": map[string]any{},
	})
	if err != nil {
		t.Fatalf("SendToHost: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("server error: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}

	raw, err := ui.ToBytes(resp.Result)
	if err != nil {
		t.Fatal(err)
	}
	// The server tool returns "Dashboard displayed" as text content.
	if !bytesContain(raw, "Dashboard displayed") {
		t.Errorf("expected 'Dashboard displayed' in result, got %s", string(raw))
	}
}

// TestAppHost_ListAllTools verifies that ListAllTools aggregates tools from
// both the MCP server and the app bridge.
func TestAppHost_ListAllTools(t *testing.T) {
	host, _, _ := setupAppHost(t)

	tools, err := host.ListAllTools(context.Background())
	if err != nil {
		t.Fatalf("ListAllTools: %v", err)
	}

	names := map[string]bool{}
	for _, td := range tools {
		names[td.Name] = true
	}

	// Server tools (from newConformanceServer)
	for _, expected := range []string{"show-dashboard", "navigate-dashboard", "dashboard-data", "plain-tool"} {
		if !names[expected] {
			t.Errorf("missing server tool %q in aggregated list", expected)
		}
	}
	// App tools
	for _, expected := range []string{"app_counter", "app_echo"} {
		if !names[expected] {
			t.Errorf("missing app tool %q in aggregated list", expected)
		}
	}
}

// TestAppHost_DynamicToolRegistration verifies that registering a new app tool
// at runtime triggers notifications/tools/list_changed, which causes AppHost
// to refresh its cached tool list.
func TestAppHost_DynamicToolRegistration(t *testing.T) {
	host, bridge, _ := setupAppHost(t)

	// Verify initial count.
	tools, _ := host.ListAppTools(context.Background())
	initialCount := len(tools)

	// Register a new tool dynamically.
	bridge.RegisterTool("app_dynamic", core.ToolDef{Description: "Added at runtime"}, func(args map[string]any) (any, error) {
		return core.ToolResult{Content: []core.Content{{Type: "text", Text: "dynamic"}}}, nil
	})

	// Wait for async refresh.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tools, _ = host.ListAppTools(context.Background())
		if len(tools) == initialCount+1 {
			// Verify the new tool is callable.
			result, err := host.CallAppTool(context.Background(), "app_dynamic", nil)
			if err != nil {
				t.Fatalf("CallAppTool(app_dynamic): %v", err)
			}
			if result.Content[0].Text != "dynamic" {
				t.Errorf("text = %q, want %q", result.Content[0].Text, "dynamic")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected %d tools after dynamic registration, got %d", initialCount+1, len(tools))
}

// TestAppHost_ToolRemoval verifies that removing an app tool triggers a
// list_changed notification and the tool disappears from the cache.
func TestAppHost_ToolRemoval(t *testing.T) {
	host, bridge, _ := setupAppHost(t)

	tools, _ := host.ListAppTools(context.Background())
	initialCount := len(tools)

	bridge.RemoveTool("app_echo")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tools, _ = host.ListAppTools(context.Background())
		if len(tools) == initialCount-1 {
			// Verify removed tool is no longer callable.
			_, err := host.CallAppTool(context.Background(), "app_echo", nil)
			if err == nil {
				t.Error("expected error calling removed tool")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected %d tools after removal, got %d", initialCount-1, len(tools))
}

func bytesContain(b []byte, sub string) bool {
	return len(b) >= len(sub) && containsStr(string(b), sub)
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
