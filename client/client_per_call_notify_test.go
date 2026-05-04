package client_test

import (
	"encoding/json"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	client "github.com/panyam/mcpkit/client"
	core "github.com/panyam/mcpkit/core"
	server "github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCallWithOptions_NotifyHookFiresOnCallStreamNotifications verifies the
// per-call notification hook (client.WithCallNotifyHook) receives notifications
// arriving on the call's POST SSE response stream.
//
// Foundation for events/stream's Stream() helper (ε-4): without per-call
// routing, notifications/events/* would only reach the session-global
// callback, forcing every consumer to demux globally. With this hook, each
// long-lived call (events/stream today, future tasks/progress streams,
// sampling/elicitation flows) gets a private notification channel scoped
// to its own response stream.
//
// Setup: a tool that emits a log notification mid-execution then returns.
// Both the global callback and the per-call hook should fire — the hook is
// additive, not a replacement.
func TestCallWithOptions_NotifyHookFiresOnCallStreamNotifications(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "per-call-test", Version: "1.0"})
	srv.RegisterTool(
		core.ToolDef{
			Name:        "emit-then-return",
			Description: "Emits a custom notification, then returns success",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, _ core.ToolRequest) (core.ToolResult, error) {
			// Use a custom method via ctx.Notify so the test doesn't depend
			// on logging/setLevel having been called (log notifications are
			// gated by the per-session log level).
			ctx.Notify("notifications/test/ping", map[string]any{"from": "handler"})
			return core.TextResult("ok"), nil
		},
	)

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	var mu sync.Mutex
	var globalCalls, hookCalls []string
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"},
		client.WithNotificationCallback(func(method string, _ any) {
			mu.Lock()
			defer mu.Unlock()
			globalCalls = append(globalCalls, method)
		}),
	)
	require.NoError(t, c.Connect())
	defer c.Close()

	hook := func(method string, _ json.RawMessage) {
		mu.Lock()
		defer mu.Unlock()
		hookCalls = append(hookCalls, method)
	}

	_, err := c.CallWithOptions("tools/call", map[string]any{
		"name":      "emit-then-return",
		"arguments": map[string]any{},
	}, client.WithCallNotifyHook(hook))
	require.NoError(t, err)

	// Both the per-call hook AND the global callback must have observed
	// the notification — the hook is additive.
	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, hookCalls, "notifications/test/ping",
		"per-call hook must receive notifications arriving on the call's SSE response stream")
	assert.Contains(t, globalCalls, "notifications/test/ping",
		"global callback must STILL fire (the per-call hook is additive, not a replacement)")
}

// TestCallWithOptions_NoHook_GlobalStillFires is the counter-test: without
// a per-call hook, the global callback continues to receive notifications
// — proves the new code path doesn't accidentally swallow them.
func TestCallWithOptions_NoHook_GlobalStillFires(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "no-hook-test", Version: "1.0"})
	srv.RegisterTool(
		core.ToolDef{
			Name:        "emit-then-return",
			Description: "Emits + returns",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, _ core.ToolRequest) (core.ToolResult, error) {
			ctx.Notify("notifications/test/ping", map[string]any{"from": "handler"})
			return core.TextResult("ok"), nil
		},
	)
	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	var mu sync.Mutex
	var seen []string
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"},
		client.WithNotificationCallback(func(method string, _ any) {
			mu.Lock()
			defer mu.Unlock()
			seen = append(seen, method)
		}),
	)
	require.NoError(t, c.Connect())
	defer c.Close()

	// CallWithOptions with no options must behave identically to Call.
	_, err := c.CallWithOptions("tools/call", map[string]any{
		"name":      "emit-then-return",
		"arguments": map[string]any{},
	})
	require.NoError(t, err)

	deadline := time.After(time.Second)
	for {
		mu.Lock()
		count := len(seen)
		mu.Unlock()
		if count > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("global notification callback never fired without per-call hook")
		case <-time.After(10 * time.Millisecond):
		}
	}

	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, seen, "notifications/test/ping")
}
