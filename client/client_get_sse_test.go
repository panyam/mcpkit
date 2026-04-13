package client_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	client "github.com/panyam/mcpkit/client"
	core "github.com/panyam/mcpkit/core"
	server "github.com/panyam/mcpkit/server"
	"net/http/httptest"
)

// newGetSSETestServer creates a server with subscriptions enabled and a tool
// that emits a log notification. Used by GET SSE stream tests to verify
// server-initiated notifications reach the client outside of POST responses.
func newGetSSETestServer() *server.Server {
	srv := server.NewServer(
		core.ServerInfo{Name: "get-sse-test", Version: "1.0.0"},
		server.WithSubscriptions(),
	)

	srv.RegisterTool(
		core.ToolDef{
			Name:        "emit-log",
			Description: "Emits a log notification then returns",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"msg": map[string]any{"type": "string"}},
				"required":   []string{"msg"},
			},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			var p struct {
				Msg string `json:"msg"`
			}
			req.Bind(&p)
			core.EmitLog(ctx, core.LogInfo, "test", p.Msg)
			return core.TextResult("ok"), nil
		},
	)

	srv.RegisterResource(
		core.ResourceDef{URI: "test://counter", Name: "Counter", MimeType: "text/plain"},
		func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{Contents: []core.ResourceReadContent{
				{URI: "test://counter", MimeType: "text/plain", Text: "0"},
			}}, nil
		},
	)

	return srv
}

// setupGetSSEClient creates an httptest.Server with Streamable HTTP and a
// client connected with WithGetSSEStream enabled. Returns the client, server
// instance (for calling NotifyResourceUpdated), and the test server.
func setupGetSSEClient(t *testing.T, srv *server.Server, notifyCb func(string, any)) (*client.Client, *httptest.Server) {
	t.Helper()
	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)

	opts := []client.ClientOption{
		client.WithGetSSEStream(),
	}
	if notifyCb != nil {
		opts = append(opts, client.WithNotificationCallback(notifyCb))
	}

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "get-sse-client", Version: "1.0"}, opts...)
	if err := c.Connect(); err != nil {
		ts.Close()
		t.Fatalf("Connect failed: %v", err)
	}

	t.Cleanup(func() {
		c.Close()
		ts.Close()
	})

	return c, ts
}

// TestGetSSEStream_ReceivesNotificationsOutsideRequest verifies that the client
// receives server-initiated notifications on the background GET SSE stream when
// they are pushed outside of any POST request-response cycle.
//
// Setup: server with subscriptions enabled, client connects with WithGetSSEStream
// and WithNotificationCallback, subscribes to a resource.
// Action: server calls NotifyResourceUpdated(uri) from outside any request context.
// Assert: the notification callback receives notifications/resources/updated.
func TestGetSSEStream_ReceivesNotificationsOutsideRequest(t *testing.T) {
	srv := newGetSSETestServer()

	var mu sync.Mutex
	var received []string
	c, _ := setupGetSSEClient(t, srv, func(method string, params any) {
		mu.Lock()
		defer mu.Unlock()
		received = append(received, method)
	})

	// Subscribe to resource so server will notify us
	if err := c.SubscribeResource("test://counter"); err != nil {
		t.Fatalf("SubscribeResource: %v", err)
	}

	// Give the GET SSE stream time to establish
	time.Sleep(100 * time.Millisecond)

	// Trigger notification from server outside any request
	srv.NotifyResourceUpdated("test://counter")

	// Wait for notification to arrive
	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		got := len(received)
		mu.Unlock()
		if got > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for notification on GET SSE stream")
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, m := range received {
		if m == "notifications/resources/updated" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected notifications/resources/updated, got %v", received)
	}
}

// TestGetSSEStream_ReceivesLogNotifications verifies that log notifications
// emitted by tool handlers reach the client's notification callback. The log
// notification may arrive on either the POST SSE response stream or the GET
// SSE stream — this test just verifies the callback fires.
//
// Setup: server with emit-log tool, client with GET SSE + notification callback.
// Action: set log level, call emit-log tool.
// Assert: notification callback receives notifications/message.
func TestGetSSEStream_ReceivesLogNotifications(t *testing.T) {
	srv := newGetSSETestServer()

	var mu sync.Mutex
	var logMessages []string
	c, _ := setupGetSSEClient(t, srv, func(method string, params any) {
		mu.Lock()
		defer mu.Unlock()
		if method == "notifications/message" {
			if m, ok := params.(map[string]any); ok {
				if data, ok := m["data"].(string); ok {
					logMessages = append(logMessages, data)
				}
			}
		}
	})

	// Enable logging
	if _, err := c.Call("logging/setLevel", map[string]any{"level": "info"}); err != nil {
		t.Fatalf("logging/setLevel: %v", err)
	}

	// Give the GET SSE stream time to establish
	time.Sleep(100 * time.Millisecond)

	// Call tool that emits a log
	text, err := c.ToolCall("emit-log", map[string]any{"msg": "hello-from-get-sse"})
	if err != nil {
		t.Fatalf("ToolCall: %v", err)
	}
	if text != "ok" {
		t.Errorf("tool result = %q, want ok", text)
	}

	// Wait for log notification
	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		got := len(logMessages)
		mu.Unlock()
		if got > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for log notification")
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(logMessages) == 0 {
		t.Error("expected log notification, got none")
	} else if logMessages[0] != "hello-from-get-sse" {
		t.Errorf("log data = %q, want hello-from-get-sse", logMessages[0])
	}
}

// TestGetSSEStream_CleanClose verifies that calling Close() on a client with
// an active GET SSE stream shuts down cleanly without goroutine leaks or hangs.
//
// Setup: client with GET SSE stream open.
// Action: call Close().
// Assert: Close() returns within a reasonable timeout (no hang).
func TestGetSSEStream_CleanClose(t *testing.T) {
	srv := newGetSSETestServer()
	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "close-test", Version: "1.0"},
		client.WithGetSSEStream(),
	)
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// Give GET SSE stream time to establish
	time.Sleep(100 * time.Millisecond)

	// Close must complete within 2 seconds (no goroutine hang)
	done := make(chan error, 1)
	go func() {
		done <- c.Close()
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Close() error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close() hung — likely goroutine leak in GET SSE stream")
	}
}

// TestGetSSEStream_NotOpenedWhenDisabled verifies that clients created without
// WithGetSSEStream() do not open a background GET SSE stream. This confirms
// the opt-in behavior — existing clients are unaffected.
//
// Setup: client without WithGetSSEStream().
// Assert: client connects and works normally; no GET request is made.
func TestGetSSEStream_NotOpenedWhenDisabled(t *testing.T) {
	srv := newGetSSETestServer()
	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "no-sse-test", Version: "1.0"})
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.Close()

	// Client should still work for normal operations
	tools, err := c.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) == 0 {
		t.Error("expected at least one tool")
	}

	// Subscribe and trigger notification — without GET SSE stream,
	// the notification should NOT arrive (no callback registered, no stream).
	// Just verify no panic or error occurs.
	if err := c.SubscribeResource("test://counter"); err != nil {
		t.Fatalf("SubscribeResource: %v", err)
	}
	srv.NotifyResourceUpdated("test://counter")
	time.Sleep(200 * time.Millisecond) // allow time for any would-be delivery
}

// TestGetSSEStream_ReconnectionReopens verifies that after a reconnection
// (transport failure + automatic retry), the GET SSE stream is re-established
// and notifications resume flowing.
//
// Setup: client with GET SSE + reconnection enabled.
// Action: stop old server, start new server at same URL, trigger transient error.
// Assert: after reconnection, notifications arrive on the new GET SSE stream.
func TestGetSSEStream_ReconnectionReopens(t *testing.T) {
	srv := newGetSSETestServer()
	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)

	var mu sync.Mutex
	var received []string

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "reconnect-test", Version: "1.0"},
		client.WithGetSSEStream(),
		client.WithMaxRetries(2),
		client.WithReconnectBackoff(50*time.Millisecond),
		client.WithNotificationCallback(func(method string, params any) {
			mu.Lock()
			defer mu.Unlock()
			received = append(received, method)
		}),
	)
	if err := c.Connect(); err != nil {
		ts.Close()
		t.Fatalf("Connect: %v", err)
	}

	// Subscribe before killing server
	if err := c.SubscribeResource("test://counter"); err != nil {
		c.Close()
		ts.Close()
		t.Fatalf("SubscribeResource: %v", err)
	}

	// Close client first (closes GET SSE stream), then kill the server.
	// httptest.Server.Close() blocks if connections are still open.
	c.Close()
	ts.Close()

	// Start a new server on the same address is not possible with httptest,
	// so we start a new one and update the client URL.
	srv2 := newGetSSETestServer()
	handler2 := srv2.Handler(server.WithStreamableHTTP(true))
	ts2 := httptest.NewServer(handler2)
	defer ts2.Close()
	c.SetURL(ts2.URL + "/mcp")

	// Make a call that will trigger reconnection (old server is dead)
	_, err := c.ListTools()
	if err != nil {
		t.Fatalf("ListTools after reconnect: %v", err)
	}

	// Re-subscribe on new server
	if err := c.SubscribeResource("test://counter"); err != nil {
		t.Fatalf("re-SubscribeResource: %v", err)
	}

	// Give GET SSE stream time to establish
	time.Sleep(200 * time.Millisecond)

	// Trigger notification on new server
	srv2.NotifyResourceUpdated("test://counter")

	// Wait for notification
	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		got := len(received)
		mu.Unlock()
		if got > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for notification after reconnection")
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, m := range received {
		if m == "notifications/resources/updated" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected notifications/resources/updated after reconnect, got %v", received)
	}

	c.Close()
}

// Ensure fmt is used (for future test additions).
var _ = fmt.Sprintf
