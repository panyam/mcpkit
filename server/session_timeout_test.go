package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/panyam/mcpkit/core"
)

// testStreamableServerWithTimeout creates an httptest.Server with a configurable
// session timeout and an echo tool. Accepts additional transport options.
func testStreamableServerWithTimeout(timeout time.Duration, opts ...TransportOption) (*httptest.Server, *Server) {
	srv := NewServer(core.ServerInfo{Name: "timeout-test", Version: "1.0"})
	srv.RegisterTool(
		core.ToolDef{Name: "echo", Description: "Echoes input", InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{"message": map[string]any{"type": "string"}},
		}},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			var p struct{ Message string `json:"message"` }
			req.Bind(&p)
			return core.TextResult("echo: " + p.Message), nil
		},
	)
	allOpts := append([]TransportOption{
		WithStreamableHTTP(true), WithSSE(false),
		WithSessionTimeout(timeout),
	}, opts...)
	ts := httptest.NewServer(srv.Handler(allOpts...))
	return ts, srv
}

// TestSessionExpiresAfterTimeout verifies that a Streamable HTTP session is
// automatically removed after the configured idle timeout elapses with no
// activity. After expiry, subsequent requests with the old session ID
// receive HTTP 404 "session not found".
func TestSessionExpiresAfterTimeout(t *testing.T) {
	ts, _ := testStreamableServerWithTimeout(50 * time.Millisecond)
	defer ts.Close()

	sessionID := streamableInit(t, ts.URL)

	// Wait for timeout to fire
	time.Sleep(120 * time.Millisecond)

	// Session should be gone
	resp, err := streamablePost(ts.URL+"/mcp", sessionID, &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/list",
	})
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 404 after timeout, got %d: %s", resp.StatusCode, body)
	}
}

// TestSessionTimeoutResetsOnActivity verifies that each POST request to a
// session resets the idle timer. If the client sends requests at intervals
// shorter than the timeout, the session should remain alive indefinitely.
func TestSessionTimeoutResetsOnActivity(t *testing.T) {
	ts, _ := testStreamableServerWithTimeout(100 * time.Millisecond)
	defer ts.Close()

	sessionID := streamableInit(t, ts.URL)

	// Send activity at 50ms intervals (< 100ms timeout) — session should survive
	for i := 0; i < 4; i++ {
		time.Sleep(50 * time.Millisecond)
		resp, err := streamablePost(ts.URL+"/mcp", sessionID, &core.Request{
			JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/list",
		})
		if err != nil {
			t.Fatalf("POST %d failed: %v", i, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST %d status = %d, want 200 (session should still be alive)", i, resp.StatusCode)
		}
	}
}

// TestSessionTimeoutPausesDuringActiveRequest verifies that the idle timer
// is paused while a tool call is executing. Even if the timeout duration
// elapses during execution, the session must not be expired because the
// acquire/release ref counting prevents it.
func TestSessionTimeoutPausesDuringActiveRequest(t *testing.T) {
	// Use a very short timeout (50ms) with a tool that takes 300ms
	srv := NewServer(core.ServerInfo{Name: "slow-test", Version: "1.0"})
	var called atomic.Bool
	srv.RegisterTool(
		core.ToolDef{Name: "slow", Description: "Takes a while"},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			called.Store(true)
			time.Sleep(150 * time.Millisecond)
			return core.TextResult("done"), nil
		},
	)
	ts := httptest.NewServer(srv.Handler(
		WithStreamableHTTP(true), WithSSE(false),
		WithSessionTimeout(30*time.Millisecond),
	))
	defer ts.Close()

	sessionID := streamableInit(t, ts.URL)

	// Call slow tool — takes 300ms, timeout is 50ms
	resp, err := streamablePost(ts.URL+"/mcp", sessionID, &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call",
		Params: json.RawMessage(`{"name":"slow"}`),
	})
	if err != nil {
		t.Fatalf("slow tool call failed: %v", err)
	}
	resp.Body.Close()

	if !called.Load() {
		t.Fatal("slow tool was not called")
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("slow tool call status = %d, want 200", resp.StatusCode)
	}

	// Session should still be alive immediately after (timer restarts on release)
	resp2, err := streamablePost(ts.URL+"/mcp", sessionID, &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "tools/list",
	})
	if err != nil {
		t.Fatalf("POST after slow tool failed: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("session died during active request: status = %d", resp2.StatusCode)
	}
}

// TestSessionDeleteStopsTimer verifies that explicitly deleting a session
// via HTTP DELETE stops the idle timer cleanly, with no crash or double-close.
func TestSessionDeleteStopsTimer(t *testing.T) {
	ts, _ := testStreamableServerWithTimeout(200 * time.Millisecond)
	defer ts.Close()

	sessionID := streamableInit(t, ts.URL)

	// DELETE the session
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/mcp", nil)
	req.Header.Set(mcpSessionIDHeader, sessionID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE status = %d, want 200", resp.StatusCode)
	}

	// Wait past the original timeout — should not panic
	time.Sleep(100 * time.Millisecond)

	// Verify session is gone
	resp2, err := streamablePost(ts.URL+"/mcp", sessionID, &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/list",
	})
	if err != nil {
		t.Fatalf("POST after DELETE failed: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 after DELETE, got %d", resp2.StatusCode)
	}
}

// TestNoTimeoutByDefault verifies that sessions without WithSessionTimeout
// persist indefinitely (until explicit DELETE or server restart). This is
// the backward-compatible default behavior.
func TestNoTimeoutByDefault(t *testing.T) {
	ts := testStreamableServer() // no WithSessionTimeout
	defer ts.Close()

	sessionID := streamableInit(t, ts.URL)

	// Wait a while
	time.Sleep(100 * time.Millisecond)

	// Session should still be alive
	resp, err := streamablePost(ts.URL+"/mcp", sessionID, &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/list",
	})
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("session expired without timeout configured: status = %d", resp.StatusCode)
	}
}
