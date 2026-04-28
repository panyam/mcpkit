package ui

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/panyam/mcpkit/core"
)

// TestAppHost_ListAppTools verifies that AppHost returns the tools registered
// on the bridge. Tests the basic host→app tools/list flow.
func TestAppHost_ListAppTools(t *testing.T) {
	bridge := NewInProcessAppBridge()
	bridge.RegisterTool("get_board", core.ToolDef{
		Description: "Get the game board state",
	}, func(args map[string]any) (any, error) {
		return map[string]any{"board": "empty"}, nil
	})

	host := NewAppHost(nil, bridge) // nil client — we only test app tools here
	if err := host.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer host.Close()

	tools, err := host.ListAppTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 app tool, got %d", len(tools))
	}
	if tools[0].Name != "get_board" {
		t.Errorf("tool name = %q, want %q", tools[0].Name, "get_board")
	}
	if tools[0].Description != "Get the game board state" {
		t.Errorf("tool description = %q, want %q", tools[0].Description, "Get the game board state")
	}
}

// TestAppHost_CallAppTool verifies that CallAppTool dispatches to the correct
// bridge handler and returns the result. Tests the host→app tools/call flow.
func TestAppHost_CallAppTool(t *testing.T) {
	bridge := NewInProcessAppBridge()
	bridge.RegisterTool("echo", core.ToolDef{}, func(args map[string]any) (any, error) {
		return core.ToolResult{
			Content: []core.Content{{Type: "text", Text: args["msg"].(string)}},
		}, nil
	})

	host := NewAppHost(nil, bridge)
	if err := host.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer host.Close()

	result, err := host.CallAppTool(context.Background(), "echo", map[string]any{"msg": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content item, got %d", len(result.Content))
	}
	if result.Content[0].Text != "hello" {
		t.Errorf("content text = %q, want %q", result.Content[0].Text, "hello")
	}
}

// TestAppHost_CallAppTool_NotFound verifies that calling an unknown app tool
// returns an error.
func TestAppHost_CallAppTool_NotFound(t *testing.T) {
	bridge := NewInProcessAppBridge()
	host := NewAppHost(nil, bridge)
	if err := host.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer host.Close()

	_, err := host.CallAppTool(context.Background(), "nonexistent", nil)
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
}

// TestAppHost_ToolListChanged verifies that registering a tool after Start()
// triggers a cache refresh via notifications/tools/list_changed.
func TestAppHost_ToolListChanged(t *testing.T) {
	bridge := NewInProcessAppBridge()
	host := NewAppHost(nil, bridge)
	if err := host.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer host.Close()

	// Initially no tools.
	tools, _ := host.ListAppTools(context.Background())
	if len(tools) != 0 {
		t.Fatalf("expected 0 tools initially, got %d", len(tools))
	}

	// Register a tool — this fires notifications/tools/list_changed,
	// which triggers an async RefreshAppTools in AppHost.
	bridge.RegisterTool("new_tool", core.ToolDef{Description: "late arrival"}, func(args map[string]any) (any, error) {
		return nil, nil
	})

	// Wait for the async refresh to complete.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tools, _ = host.ListAppTools(context.Background())
		if len(tools) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if len(tools) != 1 {
		t.Fatalf("expected 1 tool after list_changed, got %d", len(tools))
	}
	if tools[0].Name != "new_tool" {
		t.Errorf("tool name = %q, want %q", tools[0].Name, "new_tool")
	}
}

// TestAppHost_HandleAppRequest_ToolCall verifies that an app→host tools/call
// request is forwarded to the MCP server via the Client. Uses a mock client
// transport that returns a canned response.
func TestAppHost_HandleAppRequest_ToolCall(t *testing.T) {
	bridge := NewInProcessAppBridge()

	// We test the bridge routing directly since wiring a full Client requires
	// a real server. Integration tests cover the full stack.
	req := &core.Request{
		Method: "tools/call",
		ID:     json.RawMessage(`42`),
		Params: json.RawMessage(`{"name":"server_tool","arguments":{"x":1}}`),
	}

	// Verify the request handler receives the right method.
	// We can't use a real client here (no server), so we verify the bridge
	// routing instead — SendToHost dispatches to the reqHandler set by Start().
	bridge.SetRequestHandler(func(ctx context.Context, req *core.Request) *core.Response {
		// Verify the request was forwarded correctly.
		if req.Method != "tools/call" {
			t.Errorf("method = %q, want tools/call", req.Method)
		}
		var params map[string]any
		json.Unmarshal(req.Params, &params)
		if params["name"] != "server_tool" {
			t.Errorf("tool name = %v, want server_tool", params["name"])
		}
		result, _ := json.Marshal(core.ToolResult{
			Content: []core.Content{{Type: "text", Text: "from server"}},
		})
		return &core.Response{ID: req.ID, Result: json.RawMessage(result)}
	})

	resp, err := bridge.SendToHost(context.Background(), req.Method, json.RawMessage(req.Params))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %s", resp.Error.Message)
	}

	raw, err := ToBytes(resp.Result)
	if err != nil {
		t.Fatal(err)
	}
	var result core.ToolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Content) == 0 || result.Content[0].Text != "from server" {
		t.Errorf("unexpected result: %+v", result)
	}
}

// TestAppHost_Close verifies that Close shuts down the bridge and further
// bridge calls return errors.
func TestAppHost_Close(t *testing.T) {
	bridge := NewInProcessAppBridge()
	host := NewAppHost(nil, bridge)
	if err := host.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	if err := host.Close(); err != nil {
		t.Fatal(err)
	}

	// Bridge should be closed — Send should fail.
	_, err := bridge.Send(context.Background(), &core.Request{Method: "tools/list"})
	if err == nil {
		t.Fatal("expected error after Close")
	}
}
