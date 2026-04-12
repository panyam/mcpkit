package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	core "github.com/panyam/mcpkit/core"
	ssehttp "github.com/panyam/servicekit/http"
)

// TestStreamableHTTP_EmitRetryHintReachesGetSSEStream verifies that a tool
// handler calling core.EmitSSERetry during a POST request emits a "retry:"
// field on the session's long-lived GET SSE stream — not the POST response.
//
// This is the #202 follow-up completing the #72 story for Streamable HTTP.
// The test flow:
//  1. POST /mcp → initialize (JSON path, creates session)
//  2. GET /mcp (with Mcp-Session-Id header) → opens SSE stream
//  3. POST /mcp → tools/call whose handler calls EmitSSERetry(30s)
//  4. Read the GET SSE stream → expect retry: 30000
//
// The retry hint is a bare SSE event (no data, just "retry: 30000\n\n")
// delivered via the GET stream because that's the client's reconnection
// endpoint — retry hints on the POST SSE response stream would have no
// meaningful semantics.
func TestStreamableHTTP_EmitRetryHintReachesGetSSEStream(t *testing.T) {
	srv := NewServer(core.ServerInfo{Name: "retry-streamable", Version: "0.1.0"})
	srv.RegisterTool(
		core.ToolDef{
			Name:        "slow_tool",
			Description: "Emits a retry hint, then returns",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			if err := core.EmitSSERetry(ctx, 30*time.Second); err != nil {
				return core.ErrorResult("emit: " + err.Error()), nil
			}
			return core.TextResult("done"), nil
		},
	)
	ts := httptest.NewServer(srv.Handler(WithStreamableHTTP(true), WithSSE(false)))
	defer ts.Close()

	// 1. Initialize — POST with Accept: application/json (JSON path).
	initReq := &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"0"}}`),
	}
	initBody, _ := json.Marshal(initReq)
	resp, err := http.Post(ts.URL+"/mcp", "application/json", strings.NewReader(string(initBody)))
	if err != nil {
		t.Fatalf("initialize POST: %v", err)
	}
	sessionID := resp.Header.Get("Mcp-Session-Id")
	resp.Body.Close()
	if sessionID == "" {
		t.Fatal("no Mcp-Session-Id header in initialize response")
	}

	// Send initialized notification.
	notifReq := &core.Request{JSONRPC: "2.0", Method: "notifications/initialized"}
	notifBody, _ := json.Marshal(notifReq)
	req2, _ := http.NewRequest("POST", ts.URL+"/mcp", strings.NewReader(string(notifBody)))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Mcp-Session-Id", sessionID)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("initialized notification: %v", err)
	}
	resp2.Body.Close()

	// 2. Open GET SSE stream for the session.
	getReq, _ := http.NewRequest("GET", ts.URL+"/mcp", nil)
	getReq.Header.Set("Mcp-Session-Id", sessionID)
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatalf("GET SSE: %v", err)
	}
	defer getResp.Body.Close()
	if ct := getResp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("GET response Content-Type = %q, want text/event-stream", ct)
	}
	getReader := ssehttp.NewSSEEventReader(getResp.Body)

	// Small delay so the GET conn registers in the session entry.
	time.Sleep(50 * time.Millisecond)

	// 3. POST tools/call with Accept: text/event-stream (SSE path).
	callReq := &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage("2"),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"slow_tool","arguments":{}}`),
	}
	callBody, _ := json.Marshal(callReq)
	postReq, _ := http.NewRequest("POST", ts.URL+"/mcp", strings.NewReader(string(callBody)))
	postReq.Header.Set("Content-Type", "application/json")
	postReq.Header.Set("Accept", "application/json, text/event-stream")
	postReq.Header.Set("Mcp-Session-Id", sessionID)
	postResp, err := http.DefaultClient.Do(postReq)
	if err != nil {
		t.Fatalf("tools/call POST: %v", err)
	}
	defer postResp.Body.Close()

	// 4. Read the GET SSE stream — look for retry: 30000.
	var sawRetry bool
	for i := 0; i < 10; i++ {
		ev, err := readSSEEventWithRetry(getReader)
		if err != nil {
			break
		}
		if ev.Retry == 30000 {
			sawRetry = true
			break
		}
	}
	if !sawRetry {
		t.Fatalf("GET SSE stream did not receive retry: 30000 from EmitSSERetry")
	}
}

// TestStreamableHTTP_EmitRetryHintNoOpWithoutGetStream verifies that when
// no GET SSE stream is open, core.EmitSSERetry called from a POST handler
// is a silent no-op. The POST itself returns the tool result normally.
func TestStreamableHTTP_EmitRetryHintNoOpWithoutGetStream(t *testing.T) {
	srv := NewServer(core.ServerInfo{Name: "retry-noop", Version: "0.1.0"})
	var handlerCalled bool
	srv.RegisterTool(
		core.ToolDef{
			Name:        "hint_tool",
			Description: "Emits a retry hint with no GET stream",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			handlerCalled = true
			if err := core.EmitSSERetry(ctx, 5*time.Second); err != nil {
				t.Errorf("EmitSSERetry error: %v (expected nil for no-op)", err)
			}
			return core.TextResult("ok"), nil
		},
	)
	ts := httptest.NewServer(srv.Handler(WithStreamableHTTP(true), WithSSE(false)))
	defer ts.Close()

	// Initialize.
	initBody, _ := json.Marshal(&core.Request{
		JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "initialize",
		Params: json.RawMessage(`{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"0"}}`),
	})
	resp, _ := http.Post(ts.URL+"/mcp", "application/json", strings.NewReader(string(initBody)))
	sessionID := resp.Header.Get("Mcp-Session-Id")
	resp.Body.Close()

	// Send initialized.
	notifBody, _ := json.Marshal(&core.Request{JSONRPC: "2.0", Method: "notifications/initialized"})
	req2, _ := http.NewRequest("POST", ts.URL+"/mcp", strings.NewReader(string(notifBody)))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Mcp-Session-Id", sessionID)
	resp2, _ := http.DefaultClient.Do(req2)
	resp2.Body.Close()

	// POST tools/call WITHOUT opening a GET SSE stream first.
	// Use JSON Accept so the response is synchronous.
	callBody, _ := json.Marshal(&core.Request{
		JSONRPC: "2.0", ID: json.RawMessage("2"), Method: "tools/call",
		Params: json.RawMessage(`{"name":"hint_tool","arguments":{}}`),
	})
	postReq, _ := http.NewRequest("POST", ts.URL+"/mcp", strings.NewReader(string(callBody)))
	postReq.Header.Set("Content-Type", "application/json")
	postReq.Header.Set("Accept", "application/json")
	postReq.Header.Set("Mcp-Session-Id", sessionID)
	postResp, err := http.DefaultClient.Do(postReq)
	if err != nil {
		t.Fatalf("tools/call POST: %v", err)
	}
	defer postResp.Body.Close()

	if !handlerCalled {
		t.Fatal("handler was not invoked")
	}
	// Verify the tool result is returned normally even though EmitSSERetry
	// had nowhere to send the hint.
	var result core.Response
	if err := json.NewDecoder(postResp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.Error != nil {
		t.Fatalf("unexpected error: %s", result.Error.Message)
	}
}
