package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/panyam/mcpkit/core"
	ssehttp "github.com/panyam/servicekit/http"
)

// TestDetach_ToolSurvivesPerToolTimeout verifies that a detached tool
// outlives its per-tool ToolDef.Timeout. Without DetachFromClient, a
// tool that exceeds its timeout is cancelled via context.WithTimeout.
// After detaching, the timeout's cancellation is stripped and the tool
// runs to completion.
//
// This is the most practical use of DetachFromClient today: it lets a
// tool declare "I know this will take longer than my configured timeout,
// I'll manage my own deadline."
func TestDetach_ToolSurvivesPerToolTimeout(t *testing.T) {
	var mu sync.Mutex
	var completed bool

	srv := NewServer(core.ServerInfo{Name: "detach-timeout", Version: "0.1.0"})
	srv.RegisterTool(
		core.ToolDef{
			Name:        "slow_detached",
			Description: "Detaches, then runs past its timeout",
			InputSchema: map[string]any{"type": "object"},
			Timeout:     50 * time.Millisecond, // short timeout
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			// Detach — strips the per-tool timeout.
			ctx = ctx.DetachFromClient()

			// Work for longer than the timeout.
			time.Sleep(150 * time.Millisecond)

			select {
			case <-ctx.Done():
				return core.ErrorResult("cancelled"), ctx.Err()
			default:
			}

			mu.Lock()
			completed = true
			mu.Unlock()
			return core.TextResult("detached-completed"), nil
		},
	)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, _, postURL, err := connectSSE(ts, "/mcp")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer resp.Body.Close()

	postJSONRPC(t, postURL, &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "initialize",
		Params: json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"0"}}`),
	})
	// Skip reading init response.
	postJSONRPC(t, postURL, &core.Request{JSONRPC: "2.0", Method: "notifications/initialized"})

	postJSONRPC(t, postURL, &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage("2"), Method: "tools/call",
		Params: json.RawMessage(`{"name":"slow_detached","arguments":{}}`),
	})

	// Wait for tool to finish.
	time.Sleep(300 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if !completed {
		t.Fatal("detached tool did not complete — per-tool timeout killed it despite DetachFromClient")
	}
}

// TestDetach_NonDetachedToolCancelledByTimeout is the control: without
// DetachFromClient, a tool that exceeds its per-tool Timeout IS cancelled.
// This confirms DetachFromClient is opt-in and the default timeout
// behavior is unchanged.
func TestDetach_NonDetachedToolCancelledByTimeout(t *testing.T) {
	var mu sync.Mutex
	var completed bool
	var cancelled bool

	srv := NewServer(core.ServerInfo{Name: "no-detach-timeout", Version: "0.1.0"})
	srv.RegisterTool(
		core.ToolDef{
			Name:        "slow_normal",
			Description: "Does NOT detach — should be cancelled by timeout",
			InputSchema: map[string]any{"type": "object"},
			Timeout:     50 * time.Millisecond,
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			// NO DetachFromClient.
			for i := 0; i < 10; i++ {
				select {
				case <-ctx.Done():
					mu.Lock()
					cancelled = true
					mu.Unlock()
					return core.ErrorResult("cancelled"), ctx.Err()
				case <-time.After(30 * time.Millisecond):
				}
			}
			mu.Lock()
			completed = true
			mu.Unlock()
			return core.TextResult("should-not-reach"), nil
		},
	)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, _, postURL, err := connectSSE(ts, "/mcp")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer resp.Body.Close()

	postJSONRPC(t, postURL, &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "initialize",
		Params: json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"0"}}`),
	})
	postJSONRPC(t, postURL, &core.Request{JSONRPC: "2.0", Method: "notifications/initialized"})

	postJSONRPC(t, postURL, &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage("2"), Method: "tools/call",
		Params: json.RawMessage(`{"name":"slow_normal","arguments":{}}`),
	})

	time.Sleep(300 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if completed {
		t.Fatal("non-detached tool completed despite timeout — should have been cancelled")
	}
	if !cancelled {
		t.Fatal("non-detached tool was not cancelled by timeout — default behavior changed")
	}
}

// TestStreamableHTTP_DetachedToolPreservesRetryHint verifies the combined
// pattern: EmitSSERetry hint before detach, then DetachFromClient, then
// long work. The hint reaches the GET stream, and the tool completes.
func TestStreamableHTTP_DetachedToolPreservesRetryHint(t *testing.T) {
	var mu sync.Mutex
	var completed bool

	srv := NewServer(core.ServerInfo{Name: "detach-retry", Version: "0.1.0"})
	srv.RegisterTool(
		core.ToolDef{
			Name:        "combo_tool",
			Description: "Emits retry then detaches",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			_ = core.EmitSSERetry(ctx, 10*time.Second)
			ctx = ctx.DetachFromClient()
			time.Sleep(50 * time.Millisecond)
			mu.Lock()
			completed = true
			mu.Unlock()
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

	notifBody, _ := json.Marshal(&core.Request{JSONRPC: "2.0", Method: "notifications/initialized"})
	req2, _ := http.NewRequest("POST", ts.URL+"/mcp", strings.NewReader(string(notifBody)))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Mcp-Session-Id", sessionID)
	resp2, _ := http.DefaultClient.Do(req2)
	resp2.Body.Close()

	// Open GET SSE stream.
	getReq, _ := http.NewRequest("GET", ts.URL+"/mcp", nil)
	getReq.Header.Set("Mcp-Session-Id", sessionID)
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatalf("GET SSE: %v", err)
	}
	defer getResp.Body.Close()
	getReader := ssehttp.NewSSEEventReader(getResp.Body)
	time.Sleep(50 * time.Millisecond)

	// POST tools/call.
	callBody, _ := json.Marshal(&core.Request{
		JSONRPC: "2.0", ID: json.RawMessage("2"), Method: "tools/call",
		Params: json.RawMessage(`{"name":"combo_tool","arguments":{}}`),
	})
	postReq, _ := http.NewRequest("POST", ts.URL+"/mcp", strings.NewReader(string(callBody)))
	postReq.Header.Set("Content-Type", "application/json")
	postReq.Header.Set("Accept", "application/json, text/event-stream")
	postReq.Header.Set("Mcp-Session-Id", sessionID)
	postResp, err := http.DefaultClient.Do(postReq)
	if err != nil {
		t.Fatalf("tools/call POST: %v", err)
	}
	defer postResp.Body.Close()

	var sawRetry bool
	for i := 0; i < 10; i++ {
		ev, err := readSSEEventWithRetry(getReader)
		if err != nil {
			break
		}
		if ev.Retry == 10000 {
			sawRetry = true
			break
		}
	}
	if !sawRetry {
		t.Errorf("GET stream did not receive retry: 10000")
	}

	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if !completed {
		t.Fatal("detached tool did not complete")
	}
}
