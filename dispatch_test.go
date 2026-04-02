package mcpkit

import (
	"context"
	"encoding/json"
	"testing"
)

func testDispatcher() *Dispatcher {
	d := NewDispatcher(ServerInfo{Name: "test-server", Version: "1.0.0"})
	d.RegisterTool(
		ToolDef{
			Name:        "echo",
			Description: "Echoes the input",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"message": map[string]any{"type": "string"},
				},
				"required": []string{"message"},
			},
		},
		func(ctx context.Context, req ToolRequest) (ToolResult, error) {
			var args struct {
				Message string `json:"message"`
			}
			if err := req.Bind(&args); err != nil {
				return ErrorResult(err.Error()), nil
			}
			return TextResult("echo: " + args.Message), nil
		},
	)
	return d
}

func TestDispatchInitialize(t *testing.T) {
	d := testDispatcher()
	resp := d.Dispatch(context.Background(), &Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`),
	})

	if resp == nil {
		t.Fatal("response is nil")
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result["protocolVersion"] != "2024-11-05" {
		t.Errorf("protocolVersion = %v, want 2024-11-05", result["protocolVersion"])
	}
	info, ok := result["serverInfo"].(map[string]any)
	if !ok {
		t.Fatal("serverInfo not a map")
	}
	if info["name"] != "test-server" {
		t.Errorf("serverInfo.name = %v, want test-server", info["name"])
	}
	if info["version"] != "1.0.0" {
		t.Errorf("serverInfo.version = %v, want 1.0.0", info["version"])
	}
	caps, ok := result["capabilities"].(map[string]any)
	if !ok {
		t.Fatal("capabilities not a map")
	}
	if _, ok := caps["tools"]; !ok {
		t.Error("capabilities missing tools")
	}
}

func TestDispatchNotification(t *testing.T) {
	d := testDispatcher()
	for _, method := range []string{"notifications/initialized", "initialized"} {
		resp := d.Dispatch(context.Background(), &Request{
			JSONRPC: "2.0",
			Method:  method,
		})
		if resp != nil {
			t.Errorf("method %q: expected nil response, got %+v", method, resp)
		}
	}
}

func TestDispatchPing(t *testing.T) {
	d := testDispatcher()
	resp := d.Dispatch(context.Background(), &Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`99`),
		Method:  "ping",
	})
	if resp == nil {
		t.Fatal("response is nil")
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	if string(resp.ID) != "99" {
		t.Errorf("ID = %s, want 99", resp.ID)
	}
}

func TestDispatchToolsList(t *testing.T) {
	d := testDispatcher()
	resp := d.Dispatch(context.Background(), &Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`2`),
		Method:  "tools/list",
	})

	if resp == nil {
		t.Fatal("response is nil")
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	var result struct {
		Tools []ToolDef `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(result.Tools))
	}
	if result.Tools[0].Name != "echo" {
		t.Errorf("tool name = %q, want echo", result.Tools[0].Name)
	}
}

func TestDispatchToolsCall(t *testing.T) {
	d := testDispatcher()
	resp := d.Dispatch(context.Background(), &Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`3`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"echo","arguments":{"message":"hello"}}`),
	})

	if resp == nil {
		t.Fatal("response is nil")
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	var result ToolResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Error("result.IsError = true, want false")
	}
	if len(result.Content) != 1 {
		t.Fatalf("got %d content items, want 1", len(result.Content))
	}
	if result.Content[0].Text != "echo: hello" {
		t.Errorf("text = %q, want echo: hello", result.Content[0].Text)
	}
}

func TestDispatchToolsCallUnknown(t *testing.T) {
	d := testDispatcher()
	resp := d.Dispatch(context.Background(), &Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`4`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"nonexistent","arguments":{}}`),
	})

	if resp == nil {
		t.Fatal("response is nil")
	}
	if resp.Error == nil {
		t.Fatal("expected error response")
	}
	if resp.Error.Code != ErrCodeInvalidParams {
		t.Errorf("error code = %d, want %d", resp.Error.Code, ErrCodeInvalidParams)
	}
}

func TestDispatchToolsCallBadParams(t *testing.T) {
	d := testDispatcher()
	resp := d.Dispatch(context.Background(), &Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`5`),
		Method:  "tools/call",
		Params:  json.RawMessage(`not json`),
	})

	if resp == nil {
		t.Fatal("response is nil")
	}
	if resp.Error == nil {
		t.Fatal("expected error response")
	}
	if resp.Error.Code != ErrCodeInvalidParams {
		t.Errorf("error code = %d, want %d", resp.Error.Code, ErrCodeInvalidParams)
	}
}

func TestDispatchMethodNotFound(t *testing.T) {
	d := testDispatcher()
	resp := d.Dispatch(context.Background(), &Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`6`),
		Method:  "unknown/method",
	})

	if resp == nil {
		t.Fatal("response is nil")
	}
	if resp.Error == nil {
		t.Fatal("expected error response")
	}
	if resp.Error.Code != ErrCodeMethodNotFound {
		t.Errorf("error code = %d, want %d", resp.Error.Code, ErrCodeMethodNotFound)
	}
}

func TestDispatchNullID(t *testing.T) {
	d := testDispatcher()
	resp := d.Dispatch(context.Background(), &Request{
		JSONRPC: "2.0",
		ID:      nil,
		Method:  "ping",
	})
	if resp == nil {
		t.Fatal("response is nil")
	}
	if string(resp.ID) != "null" {
		t.Errorf("ID = %s, want null", resp.ID)
	}
}

func TestDispatchToolOrder(t *testing.T) {
	d := NewDispatcher(ServerInfo{Name: "test", Version: "1.0"})
	for _, name := range []string{"charlie", "alpha", "bravo"} {
		d.RegisterTool(ToolDef{Name: name, Description: name}, func(ctx context.Context, req ToolRequest) (ToolResult, error) {
			return TextResult(name), nil
		})
	}

	resp := d.Dispatch(context.Background(), &Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/list",
	})

	var result struct {
		Tools []ToolDef `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Tools) != 3 {
		t.Fatalf("got %d tools, want 3", len(result.Tools))
	}
	// tools/list should preserve registration order
	want := []string{"charlie", "alpha", "bravo"}
	for i, name := range want {
		if result.Tools[i].Name != name {
			t.Errorf("tools[%d].Name = %q, want %q", i, result.Tools[i].Name, name)
		}
	}
}
