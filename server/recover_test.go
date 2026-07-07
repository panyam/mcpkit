package server

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	core "github.com/panyam/mcpkit/core"
)

// Test_Issue420_PanicHandlerReturnsError verifies that a tool handler which
// panics is recovered at the dispatch entry point and surfaces as a JSON-RPC
// -32603 internal error, rather than crashing the host process.
func Test_Issue420_PanicHandlerReturnsError(t *testing.T) {
	d := NewDispatcher(core.ServerInfo{Name: "s", Version: "1"})
	d.RegisterTool(
		core.ToolDef{Name: "boom", Description: "panics", InputSchema: map[string]any{"type": "object"}},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			panic("kaboom")
		},
	)
	initDispatcher(d)

	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call",
		Params: json.RawMessage(`{"name":"boom","arguments":{}}`),
	})
	if resp == nil || resp.Error == nil {
		t.Fatalf("expected an error response from a panicking handler, got %+v", resp)
	}
	if resp.Error.Code != core.ErrCodeInternal {
		t.Errorf("error code = %d, want %d (-32603)", resp.Error.Code, core.ErrCodeInternal)
	}
	// The panic detail must NOT leak to the wire.
	if resp.Error.Message != "internal error" {
		t.Errorf("error message = %q, want generic %q", resp.Error.Message, "internal error")
	}
}

// Test_Issue420_PanicNotificationNoResponse verifies that a panic while
// dispatching a notification (no ID) yields nil, not a spurious error frame.
func Test_Issue420_PanicNotificationNoResponse(t *testing.T) {
	d := NewDispatcher(core.ServerInfo{Name: "s", Version: "1"})
	// A custom handler registered for a notification-style method that panics.
	d.RegisterTool(
		core.ToolDef{Name: "boom", Description: "panics", InputSchema: map[string]any{"type": "object"}},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) { panic("x") },
	)
	initDispatcher(d)
	// tools/call with no ID (notification shape) — panic must recover to nil.
	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", Method: "tools/call",
		Params: json.RawMessage(`{"name":"boom","arguments":{}}`),
	})
	if resp != nil {
		t.Errorf("expected nil for a panicking notification, got %+v", resp)
	}
}

// Test_Issue420_SafeGoRecovers verifies that safeGo swallows a panic in a
// background goroutine — the test process reaches the assertion instead of
// crashing.
func Test_Issue420_SafeGoRecovers(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	safeGo("test.panic", func() {
		defer wg.Done()
		panic("background boom")
	})
	wg.Wait() // if safeGo didn't recover, the process would have aborted
}
