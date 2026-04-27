package ui

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/panyam/mcpkit/core"
)

// resultBytes extracts the raw JSON bytes from a Response.Result, handling
// both json.RawMessage and []byte (they're the same underlying type but Go's
// runtime type assertion through any is concrete-type-exact).
func resultBytes(t *testing.T, resp *core.Response) []byte {
	t.Helper()
	switch v := resp.Result.(type) {
	case json.RawMessage:
		return []byte(v)
	case []byte:
		return v
	default:
		raw, err := json.Marshal(resp.Result)
		if err != nil {
			t.Fatalf("cannot marshal result: %v", err)
		}
		return raw
	}
}

// TestBridge_RegisterTool_SendsNotification verifies that registering a tool
// fires a notifications/tools/list_changed notification to the host.
func TestBridge_RegisterTool_SendsNotification(t *testing.T) {
	b := NewInProcessAppBridge()

	var notified atomic.Int32
	b.SetNotificationHandler(func(method string, params json.RawMessage) {
		if method == "notifications/tools/list_changed" {
			notified.Add(1)
		}
	})

	b.RegisterTool("my-tool", core.ToolDef{Description: "test"}, func(args map[string]any) (any, error) {
		return nil, nil
	})

	if notified.Load() != 1 {
		t.Fatalf("expected 1 notification, got %d", notified.Load())
	}
}

// TestBridge_RemoveTool_SendsNotification verifies that removing a tool also
// fires a notifications/tools/list_changed notification.
func TestBridge_RemoveTool_SendsNotification(t *testing.T) {
	b := NewInProcessAppBridge()

	var notified atomic.Int32
	b.SetNotificationHandler(func(method string, params json.RawMessage) {
		if method == "notifications/tools/list_changed" {
			notified.Add(1)
		}
	})

	b.RegisterTool("my-tool", core.ToolDef{}, func(args map[string]any) (any, error) {
		return nil, nil
	})
	b.RemoveTool("my-tool")

	if notified.Load() != 2 {
		t.Fatalf("expected 2 notifications (register + remove), got %d", notified.Load())
	}
}

// TestBridge_Send_ToolsList verifies that tools/list returns all registered tools.
func TestBridge_Send_ToolsList(t *testing.T) {
	b := NewInProcessAppBridge()
	b.RegisterTool("alpha", core.ToolDef{Description: "Tool A"}, func(args map[string]any) (any, error) {
		return nil, nil
	})
	b.RegisterTool("beta", core.ToolDef{Description: "Tool B"}, func(args map[string]any) (any, error) {
		return nil, nil
	})

	resp, err := b.Send(context.Background(), &core.Request{
		Method: "tools/list",
		ID:     json.RawMessage(`1`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %s", resp.Error.Message)
	}

	var result struct {
		Tools []core.ToolDef `json:"tools"`
	}
	if err := json.Unmarshal(resultBytes(t, resp), &result); err != nil {
		t.Fatal(err)
	}

	if len(result.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(result.Tools))
	}

	names := map[string]bool{}
	for _, td := range result.Tools {
		names[td.Name] = true
	}
	if !names["alpha"] || !names["beta"] {
		t.Errorf("expected tools alpha and beta, got %v", names)
	}
}

// TestBridge_Send_ToolsCall verifies that tools/call dispatches to the correct handler.
func TestBridge_Send_ToolsCall(t *testing.T) {
	b := NewInProcessAppBridge()
	b.RegisterTool("echo", core.ToolDef{}, func(args map[string]any) (any, error) {
		return map[string]any{"echoed": args["msg"]}, nil
	})

	params, _ := json.Marshal(map[string]any{
		"name":      "echo",
		"arguments": map[string]any{"msg": "hello"},
	})
	resp, err := b.Send(context.Background(), &core.Request{
		Method: "tools/call",
		ID:     json.RawMessage(`2`),
		Params: params,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %s", resp.Error.Message)
	}

	var result map[string]any
	if err := json.Unmarshal(resultBytes(t, resp), &result); err != nil {
		t.Fatal(err)
	}
	if result["echoed"] != "hello" {
		t.Errorf("expected echoed=hello, got %v", result["echoed"])
	}
}

// TestBridge_Send_UnknownTool verifies that calling an unregistered tool returns an error.
func TestBridge_Send_UnknownTool(t *testing.T) {
	b := NewInProcessAppBridge()

	params, _ := json.Marshal(map[string]any{
		"name":      "nonexistent",
		"arguments": map[string]any{},
	})
	resp, err := b.Send(context.Background(), &core.Request{
		Method: "tools/call",
		ID:     json.RawMessage(`3`),
		Params: params,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil {
		t.Fatal("expected error for unknown tool")
	}
	if resp.Error.Code != core.ErrCodeInvalidParams {
		t.Errorf("error code = %d, want %d", resp.Error.Code, core.ErrCodeInvalidParams)
	}
}

// TestBridge_RemoveTool verifies that a removed tool no longer appears in tools/list.
func TestBridge_RemoveTool(t *testing.T) {
	b := NewInProcessAppBridge()
	b.RegisterTool("temp", core.ToolDef{}, func(args map[string]any) (any, error) {
		return nil, nil
	})
	b.RemoveTool("temp")

	resp, err := b.Send(context.Background(), &core.Request{
		Method: "tools/list",
		ID:     json.RawMessage(`4`),
	})
	if err != nil {
		t.Fatal(err)
	}

	var result struct {
		Tools []core.ToolDef `json:"tools"`
	}
	if err := json.Unmarshal(resultBytes(t, resp), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Tools) != 0 {
		t.Errorf("expected 0 tools after remove, got %d", len(result.Tools))
	}
}

// TestBridge_SendToHost verifies that app→host requests are forwarded to the
// request handler set by AppHost.
func TestBridge_SendToHost(t *testing.T) {
	b := NewInProcessAppBridge()
	b.SetRequestHandler(func(ctx context.Context, req *core.Request) *core.Response {
		result, _ := json.Marshal(map[string]string{"method": req.Method})
		return &core.Response{Result: result}
	})
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}

	resp, err := b.SendToHost(context.Background(), "tools/call", map[string]any{"name": "server-tool"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %s", resp.Error.Message)
	}

	var result map[string]string
	if err := json.Unmarshal(resultBytes(t, resp), &result); err != nil {
		t.Fatal(err)
	}
	if result["method"] != "tools/call" {
		t.Errorf("expected method=tools/call, got %s", result["method"])
	}
}

// TestBridge_ClosedBridge verifies that Send returns an error after Close.
func TestBridge_ClosedBridge(t *testing.T) {
	b := NewInProcessAppBridge()
	b.Close()

	_, err := b.Send(context.Background(), &core.Request{Method: "tools/list"})
	if err == nil {
		t.Fatal("expected error on closed bridge")
	}
}
