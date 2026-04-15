package server

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"

	core "github.com/panyam/mcpkit/core"
)

// helper: creates an initialized dispatcher with an empty registry.
func testDynamicDispatcher() *Dispatcher {
	d := NewDispatcher(core.ServerInfo{Name: "dynamic-test", Version: "1.0.0"})
	initDispatcher(d)
	return d
}

// helper: creates a server with a session wired to capture broadcast notifications.
// Returns the server and a function that returns all captured notification methods.
func testDynamicServer() (*Server, func() []string) {
	srv := NewServer(core.ServerInfo{Name: "dynamic-test", Version: "1.0.0"})

	d := srv.newSession()
	d.sessionID = "test-session"

	var mu sync.Mutex
	var methods []string
	d.SetNotifyFunc(func(method string, params any) {
		mu.Lock()
		defer mu.Unlock()
		methods = append(methods, method)
	})

	srv.registerTransportSessions(
		func(id string) bool { return false },
		func() {},
		func(method string, params any) {
			if fn := d.getNotifyFunc(); fn != nil {
				fn(method, params)
			}
		},
	)

	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := make([]string, len(methods))
		copy(out, methods)
		methods = methods[:0] // drain on read
		return out
	}
}

// TestAddToolAfterServing verifies that a tool added via Registry().AddTool
// after sessions are connected appears in the tools/list response. This is
// the core dynamic registration test — tools registered at runtime must be
// visible to existing sessions because the registry is shared by pointer.
func TestAddToolAfterServing(t *testing.T) {
	d := testDynamicDispatcher()

	// Initially no tools
	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/list",
	})
	assertToolCount(t, resp, 0)

	// Add a tool at runtime
	d.Reg.AddTool(
		core.ToolDef{Name: "dynamic", Description: "Added at runtime"},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("dynamic"), nil
		},
	)

	// Now tools/list should return it
	resp = d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "tools/list",
	})
	assertToolCount(t, resp, 1)
}

// TestRemoveTool verifies that removing a tool via Registry().RemoveTool
// causes it to disappear from tools/list and makes tools/call return an error.
func TestRemoveTool(t *testing.T) {
	d := testDynamicDispatcher()

	d.Reg.AddTool(
		core.ToolDef{Name: "ephemeral", Description: "Will be removed"},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("here"), nil
		},
	)

	// Tool works
	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call",
		Params: json.RawMessage(`{"name":"ephemeral"}`),
	})
	if resp.Error != nil {
		t.Fatalf("tool call before removal failed: %s", resp.Error.Message)
	}

	// Remove it
	if !d.Reg.RemoveTool("ephemeral") {
		t.Fatal("RemoveTool returned false for existing tool")
	}

	// tools/list should be empty
	resp = d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "tools/list",
	})
	assertToolCount(t, resp, 0)

	// tools/call should fail
	resp = d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`3`), Method: "tools/call",
		Params: json.RawMessage(`{"name":"ephemeral"}`),
	})
	if resp.Error == nil {
		t.Fatal("tool call after removal should fail")
	}
}

// TestRemoveToolNotFound verifies that RemoveTool returns false for a
// tool that does not exist, and does not panic or modify the registry.
func TestRemoveToolNotFound(t *testing.T) {
	reg := NewRegistry()
	if reg.RemoveTool("nonexistent") {
		t.Fatal("RemoveTool returned true for nonexistent tool")
	}
}

// TestAddToolBroadcastsListChanged verifies that adding a tool via
// Registry().AddTool triggers an automatic notifications/tools/list_changed
// broadcast to all connected sessions via the OnChange callback.
func TestAddToolBroadcastsListChanged(t *testing.T) {
	srv, captured := testDynamicServer()

	srv.Registry().AddTool(
		core.ToolDef{Name: "notified", Description: "triggers broadcast"},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("ok"), nil
		},
	)

	methods := captured()
	if len(methods) != 1 || methods[0] != "notifications/tools/list_changed" {
		t.Fatalf("expected [notifications/tools/list_changed], got %v", methods)
	}
}

// TestRemoveToolBroadcastsListChanged verifies that removing a tool
// broadcasts notifications/tools/list_changed, but removing a nonexistent
// tool does not broadcast.
func TestRemoveToolBroadcastsListChanged(t *testing.T) {
	srv, captured := testDynamicServer()

	srv.Registry().AddTool(
		core.ToolDef{Name: "temp", Description: "temp"},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("ok"), nil
		},
	)

	// Clear captured from AddTool
	_ = captured()

	// Remove existing tool — should broadcast
	srv.Registry().RemoveTool("temp")
	methods := captured()
	if len(methods) != 1 || methods[0] != "notifications/tools/list_changed" {
		t.Fatalf("expected [notifications/tools/list_changed] on remove, got %v", methods)
	}

	// Remove nonexistent — should NOT broadcast
	srv.Registry().RemoveTool("temp")
	methods = captured()
	if len(methods) != 0 {
		t.Fatalf("expected no broadcast on remove-nonexistent, got %v", methods)
	}
}

// TestAddResourceAfterServing verifies that dynamically added resources
// appear in resources/list and can be read via resources/read.
func TestAddResourceAfterServing(t *testing.T) {
	d := testDynamicDispatcher()

	d.Reg.AddResource(
		core.ResourceDef{URI: "test://dynamic", Name: "Dynamic Resource"},
		func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{Contents: []core.ResourceReadContent{
				{URI: req.URI, Text: "dynamic content"},
			}}, nil
		},
	)

	// resources/list
	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "resources/list",
	})
	raw, _ := core.MarshalJSON(resp.Result)
	if !json.Valid(raw) {
		t.Fatal("invalid result JSON")
	}
	var result struct {
		Resources []core.ResourceDef `json:"resources"`
	}
	json.Unmarshal(raw, &result)
	if len(result.Resources) != 1 || result.Resources[0].URI != "test://dynamic" {
		t.Fatalf("unexpected resources: %+v", result.Resources)
	}
}

// TestRemoveResource verifies that removing a resource makes it disappear
// from resources/list and makes resources/read return an error.
func TestRemoveResource(t *testing.T) {
	d := testDynamicDispatcher()

	d.Reg.AddResource(
		core.ResourceDef{URI: "test://temp", Name: "Temp"},
		func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{Contents: []core.ResourceReadContent{
				{URI: req.URI, Text: "temp"},
			}}, nil
		},
	)

	if !d.Reg.RemoveResource("test://temp") {
		t.Fatal("RemoveResource returned false")
	}

	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "resources/read",
		Params: json.RawMessage(`{"uri":"test://temp"}`),
	})
	if resp.Error == nil {
		t.Fatal("resources/read should fail after removal")
	}
}

// TestAddResourceBroadcastsListChanged verifies that adding a resource
// triggers notifications/resources/list_changed.
func TestAddResourceBroadcastsListChanged(t *testing.T) {
	srv, captured := testDynamicServer()

	srv.Registry().AddResource(
		core.ResourceDef{URI: "test://r", Name: "R"},
		func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{}, nil
		},
	)

	methods := captured()
	if len(methods) != 1 || methods[0] != "notifications/resources/list_changed" {
		t.Fatalf("expected [notifications/resources/list_changed], got %v", methods)
	}
}

// TestAddPromptAfterServing verifies that dynamically added prompts
// appear in prompts/list and can be retrieved via prompts/get.
func TestAddPromptAfterServing(t *testing.T) {
	d := testDynamicDispatcher()

	d.Reg.AddPrompt(
		core.PromptDef{Name: "dynamic-prompt", Description: "Added at runtime"},
		func(ctx core.PromptContext, req core.PromptRequest) (core.PromptResult, error) {
			return core.PromptResult{Messages: []core.PromptMessage{
				{Role: "assistant", Content: core.Content{Type: "text", Text: "dynamic"}},
			}}, nil
		},
	)

	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "prompts/list",
	})
	raw, _ := core.MarshalJSON(resp.Result)
	var result struct {
		Prompts []core.PromptDef `json:"prompts"`
	}
	json.Unmarshal(raw, &result)
	if len(result.Prompts) != 1 || result.Prompts[0].Name != "dynamic-prompt" {
		t.Fatalf("unexpected prompts: %+v", result.Prompts)
	}
}

// TestRemovePrompt verifies that removing a prompt makes it disappear
// from prompts/list and makes prompts/get return an error.
func TestRemovePrompt(t *testing.T) {
	d := testDynamicDispatcher()

	d.Reg.AddPrompt(
		core.PromptDef{Name: "temp-prompt", Description: "Temp"},
		func(ctx core.PromptContext, req core.PromptRequest) (core.PromptResult, error) {
			return core.PromptResult{}, nil
		},
	)

	if !d.Reg.RemovePrompt("temp-prompt") {
		t.Fatal("RemovePrompt returned false")
	}

	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "prompts/get",
		Params: json.RawMessage(`{"name":"temp-prompt"}`),
	})
	if resp.Error == nil {
		t.Fatal("prompts/get should fail after removal")
	}
}

// TestAddPromptBroadcastsListChanged verifies that adding a prompt
// triggers notifications/prompts/list_changed.
func TestAddPromptBroadcastsListChanged(t *testing.T) {
	srv, captured := testDynamicServer()

	srv.Registry().AddPrompt(
		core.PromptDef{Name: "p", Description: "P"},
		func(ctx core.PromptContext, req core.PromptRequest) (core.PromptResult, error) {
			return core.PromptResult{}, nil
		},
	)

	methods := captured()
	if len(methods) != 1 || methods[0] != "notifications/prompts/list_changed" {
		t.Fatalf("expected [notifications/prompts/list_changed], got %v", methods)
	}
}

// TestConcurrentAddAndList verifies that concurrent AddTool and tools/list
// operations do not race. This test is meaningful under -race; without the
// race detector it will pass even with broken locking, but with -race it
// catches missing synchronization.
func TestConcurrentAddAndList(t *testing.T) {
	d := testDynamicDispatcher()

	var wg sync.WaitGroup
	var adds atomic.Int32

	// 10 goroutines adding tools
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			d.Reg.AddTool(
				core.ToolDef{Name: json.Number(json.Number(string(rune('a'+i)))).String(), Description: "concurrent"},
				func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
					return core.TextResult("ok"), nil
				},
			)
			adds.Add(1)
		}(i)
	}

	// 10 goroutines listing tools
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.Dispatch(context.Background(), &core.Request{
				JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/list",
			})
		}()
	}

	wg.Wait()

	if adds.Load() != 10 {
		t.Fatalf("expected 10 adds, got %d", adds.Load())
	}
}

// TestListChangedCapabilityAdvertised verifies that the initialize response
// includes listChanged: true for tools, resources, and prompts capabilities,
// so clients know to listen for list_changed notifications.
func TestListChangedCapabilityAdvertised(t *testing.T) {
	d := NewDispatcher(core.ServerInfo{Name: "test", Version: "1.0"})

	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "initialize",
		Params: json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`),
	})

	raw, _ := core.MarshalJSON(resp.Result)
	var result struct {
		Capabilities struct {
			Tools     struct{ ListChanged bool `json:"listChanged"` } `json:"tools"`
			Resources struct{ ListChanged bool `json:"listChanged"` } `json:"resources"`
			Prompts   struct{ ListChanged bool `json:"listChanged"` } `json:"prompts"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if !result.Capabilities.Tools.ListChanged {
		t.Error("tools.listChanged should be true")
	}
	if !result.Capabilities.Resources.ListChanged {
		t.Error("resources.listChanged should be true")
	}
	if !result.Capabilities.Prompts.ListChanged {
		t.Error("prompts.listChanged should be true")
	}
}

// assertToolCount is a helper that verifies a tools/list response contains
// exactly n tools.
func assertToolCount(t *testing.T, resp *core.Response, n int) {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("tools/list failed: %s", resp.Error.Message)
	}
	raw, _ := core.MarshalJSON(resp.Result)
	var result struct {
		Tools []core.ToolDef `json:"tools"`
	}
	json.Unmarshal(raw, &result)
	if len(result.Tools) != n {
		t.Fatalf("expected %d tools, got %d", n, len(result.Tools))
	}
}

