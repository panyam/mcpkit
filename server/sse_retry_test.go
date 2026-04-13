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

// readSSEEventWithRetry reads the next meaningful SSE event from the reader,
// preserving the Retry field (which the existing readSSEEvent helper drops).
// Comment-only keepalives are skipped. Bare retry events (Event="", Data="",
// Retry>0) are NOT skipped — they are the whole point of this helper.
func readSSEEventWithRetry(r *ssehttp.SSEEventReader) (ssehttp.SSEReadEvent, error) {
	for {
		ev, err := r.ReadEvent()
		if err != nil {
			return ssehttp.SSEReadEvent{}, err
		}
		if ev.Event == "" && ev.Data == "" && ev.Retry == 0 && ev.Comment != "" {
			continue
		}
		return ev, nil
	}
}

// postJSONRPC posts a JSON-RPC request to the POST URL extracted from an SSE
// endpoint event. The actual result is delivered on the SSE stream, not the
// HTTP response.
func postJSONRPC(t *testing.T, postURL string, req *core.Request) {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(postURL, "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("unexpected status %d", resp.StatusCode)
	}
}

// TestSSE_EmitRetryHintReachesClient is the B2 happy-path integration test:
// a tool handler calls core.EmitSSERetry, and the raw SSE "retry:" field
// appears in the client's incoming stream. Exercises the full path:
// core.EmitSSERetry -> session.sseRetry -> mcpSSEConn.SendRetry ->
// servicekit writer -> client parser.
//
// Issue #72. Covers the 2024-11-05 SSE transport; Streamable HTTP retry
// support is a follow-up.
func TestSSE_EmitRetryHintReachesClient(t *testing.T) {
	srv := NewServer(core.ServerInfo{Name: "retry-test", Version: "0.1.0"})
	srv.RegisterTool(
		core.ToolDef{
			Name:        "long_running",
			Description: "Emits a retry hint, then returns",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			if err := core.EmitSSERetry(ctx, 30*time.Second); err != nil {
				return core.ErrorResult("emit: " + err.Error()), nil
			}
			return core.TextResult("done"), nil
		},
	)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, reader, postURL, err := connectSSE(ts, "/mcp")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer resp.Body.Close()

	postJSONRPC(t, postURL, &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"0"}}`),
	})
	if _, err := readSSEEventWithRetry(reader); err != nil {
		t.Fatalf("read init response: %v", err)
	}
	postJSONRPC(t, postURL, &core.Request{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	})

	postJSONRPC(t, postURL, &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage("2"),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"long_running","arguments":{}}`),
	})

	// Read until we see retry: 30000. SendRetry emits a bare event (no
	// data), followed by the tool response as a separate data event.
	var sawRetry bool
	for i := 0; i < 8; i++ {
		ev, err := readSSEEventWithRetry(reader)
		if err != nil {
			break
		}
		if ev.Retry == 30000 {
			sawRetry = true
			break
		}
	}
	if !sawRetry {
		t.Fatalf("did not receive retry: 30000 from EmitSSERetry call")
	}
}

// TestSSE_EmitRetryHintNoOpOnNonSSEPath verifies that a tool handler calling
// core.EmitSSERetry under a transport without SSE retry wiring (stdio,
// in-process, Streamable HTTP JSON path) is a silent no-op. Handlers must
// be free to call EmitSSERetry unconditionally without branching on
// transport type.
func TestSSE_EmitRetryHintNoOpOnNonSSEPath(t *testing.T) {
	// Raw dispatcher with no SSE wiring simulates the non-SSE path.
	d := NewDispatcher(core.ServerInfo{Name: "t", Version: "0"})
	var called bool
	d.RegisterTool(
		core.ToolDef{Name: "t", InputSchema: map[string]any{"type": "object"}},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			called = true
			if err := core.EmitSSERetry(ctx, 5*time.Second); err != nil {
				t.Errorf("EmitSSERetry on no-sse-hint ctx: %v", err)
			}
			return core.TextResult("ok"), nil
		},
	)
	initDispatcher(d)
	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/call",
		Params: json.RawMessage(`{"name":"t","arguments":{}}`),
	})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %s", resp.Error.Message)
	}
	if !called {
		t.Fatal("tool handler was not invoked")
	}
}
