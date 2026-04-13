package server

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	core "github.com/panyam/mcpkit/core"
)

// TestNotifyResourceUpdatedFromHandler verifies the full pipeline: a tool
// handler calls core.NotifyResourceUpdated(ctx, uri), which routes through
// the subscription registry to deliver notifications/resources/updated to
// all sessions subscribed to that URI — not just the caller's session.
//
// Setup: two sessions (d1, d2). d1 subscribes to "test://res". d2 does not.
// A tool handler running in d1's context calls NotifyResourceUpdated.
// d1 should receive the notification. d2 should not.
//
// Issue #208.
func TestNotifyResourceUpdatedFromHandler(t *testing.T) {
	srv := NewServer(
		core.ServerInfo{Name: "notify-test", Version: "1.0"},
		WithSubscriptions(),
	)

	var handlerCalled bool
	srv.RegisterTool(
		core.ToolDef{Name: "mutate", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			handlerCalled = true
			core.NotifyResourceUpdated(ctx, "test://res")
			return core.TextResult("ok"), nil
		},
	)
	srv.RegisterResource(
		core.ResourceDef{URI: "test://res", Name: "Test"},
		func(ctx context.Context, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{Contents: []core.ResourceReadContent{{URI: req.URI, Text: "data"}}}, nil
		},
	)

	// Create two sessions.
	d1 := srv.newSession()
	d1.sessionID = "session-1"
	initDispatcher(d1)

	d2 := srv.newSession()
	d2.sessionID = "session-2"
	initDispatcher(d2)

	// Track notifications per session.
	var mu sync.Mutex
	notifications := map[string][]string{} // sessionID → list of notified URIs

	d1.SetNotifyFunc(func(method string, params any) {
		if method == "notifications/resources/updated" {
			raw, _ := json.Marshal(params)
			var p struct{ URI string `json:"uri"` }
			json.Unmarshal(raw, &p)
			mu.Lock()
			notifications["session-1"] = append(notifications["session-1"], p.URI)
			mu.Unlock()
		}
	})
	d2.SetNotifyFunc(func(method string, params any) {
		if method == "notifications/resources/updated" {
			mu.Lock()
			notifications["session-2"] = append(notifications["session-2"], "any")
			mu.Unlock()
		}
	})

	// d1 subscribes to "test://res". d2 does not.
	d1.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "resources/subscribe",
		Params: json.RawMessage(`{"uri":"test://res"}`),
	})

	// Call the tool via d1's dispatch context. The tool handler calls
	// core.NotifyResourceUpdated(ctx, "test://res").
	resp := srv.dispatchWith(d1, context.Background(), nil, &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "tools/call",
		Params: json.RawMessage(`{"name":"mutate","arguments":{}}`),
	})
	if resp.Error != nil {
		t.Fatalf("tool error: %s", resp.Error.Message)
	}
	if !handlerCalled {
		t.Fatal("handler was not invoked")
	}

	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(notifications["session-1"]) != 1 || notifications["session-1"][0] != "test://res" {
		t.Errorf("session-1 notifications = %v, want [test://res]", notifications["session-1"])
	}
	if len(notifications["session-2"]) != 0 {
		t.Errorf("session-2 should not have received notifications, got %v", notifications["session-2"])
	}
}
