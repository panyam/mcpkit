package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	core "github.com/panyam/mcpkit/core"
	ssehttp "github.com/panyam/servicekit/http"
)

// sseEvent represents a parsed SSE event from the stream.
type sseEvent struct {
	Event string
	Data  string
}

// readSSEEvent reads the next SSE event from an SSEEventReader, skipping
// comment-only and empty events (keepalives).
func readSSEEvent(r *ssehttp.SSEEventReader) (sseEvent, error) {
	for {
		ev, err := r.ReadEvent()
		if err != nil {
			return sseEvent{}, err
		}
		if ev.Event == "" && ev.Data == "" {
			continue // skip comment-only events
		}
		return sseEvent{Event: ev.Event, Data: ev.Data}, nil
	}
}

// testMCPServer creates an httptest.Server with an echo tool and the given
// transport options. Note: Cannot use testutil.NewTestServer due to import
// cycle (package server tests cannot import testutil which imports server).
func testMCPServer(opts ...TransportOption) (*httptest.Server, *Server) {
	srv := NewServer(core.ServerInfo{Name: "test-sse", Version: "0.1.0"})
	srv.RegisterTool(
		core.ToolDef{
			Name:        "echo",
			Description: "Echoes the input",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"message": map[string]any{"type": "string"},
				},
			},
		},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			var args struct {
				Message string `json:"message"`
			}
			req.Bind(&args)
			return core.TextResult("echo: " + args.Message), nil
		},
	)
	handler := srv.Handler(opts...)
	ts := httptest.NewServer(handler)
	return ts, srv
}

// connectSSE opens an SSE connection to the test server and reads the endpoint event.
// Returns the SSE response (keep open for reading more events), the SSE reader for
// subsequent events, the POST URL from the endpoint event, and any error.
func connectSSE(ts *httptest.Server, prefix string) (*http.Response, *ssehttp.SSEEventReader, string, error) {
	resp, err := http.Get(ts.URL + prefix + "/sse")
	if err != nil {
		return nil, nil, "", fmt.Errorf("GET /sse: %w", err)
	}

	reader := ssehttp.NewSSEEventReader(resp.Body)
	ev, err := readSSEEvent(reader)
	if err != nil {
		resp.Body.Close()
		return nil, nil, "", fmt.Errorf("reading endpoint event: %w", err)
	}
	if ev.Event != "endpoint" {
		resp.Body.Close()
		return nil, nil, "", fmt.Errorf("expected endpoint event, got %q", ev.Event)
	}

	return resp, reader, ev.Data, nil
}

// connectSSEWithAuth is like connectSSE but sends an Authorization: Bearer header.
func connectSSEWithAuth(ts *httptest.Server, prefix string, token string) (*http.Response, *ssehttp.SSEEventReader, string, error) {
	req, err := http.NewRequest("GET", ts.URL+prefix+"/sse", nil)
	if err != nil {
		return nil, nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, "", fmt.Errorf("GET /sse: %w", err)
	}

	reader := ssehttp.NewSSEEventReader(resp.Body)
	ev, err := readSSEEvent(reader)
	if err != nil {
		resp.Body.Close()
		return nil, nil, "", fmt.Errorf("reading endpoint event: %w", err)
	}
	if ev.Event != "endpoint" {
		resp.Body.Close()
		return nil, nil, "", fmt.Errorf("expected endpoint event, got %q", ev.Event)
	}

	return resp, reader, ev.Data, nil
}

// postJSON sends a JSON-RPC request to the given URL and returns the HTTP response.
func postJSON(url string, body any) (*http.Response, error) {
	raw, _ := json.Marshal(body)
	return http.Post(url, "application/json", bytes.NewReader(raw))
}

// TestSSEEndpointEvent verifies that connecting to GET /sse returns an SSE stream
// with an initial "endpoint" event containing the POST URL with a sessionId parameter.
func TestSSEEndpointEvent(t *testing.T) {
	ts, _ := testMCPServer()
	defer ts.Close()

	sseResp, _, postURL, err := connectSSE(ts, "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	defer sseResp.Body.Close()

	if !strings.Contains(postURL, "/mcp/message?sessionId=") {
		t.Errorf("endpoint URL = %q, want to contain /mcp/message?sessionId=", postURL)
	}
	if !strings.HasPrefix(postURL, "http://") {
		t.Errorf("endpoint URL = %q, want http:// prefix", postURL)
	}
}

// TestSSEInitAndToolCall verifies the full MCP lifecycle over HTTP+SSE:
// connect SSE → initialize → notifications/initialized → tools/call → read response.
func TestSSEInitAndToolCall(t *testing.T) {
	ts, _ := testMCPServer()
	defer ts.Close()

	sseResp, reader, postURL, err := connectSSE(ts, "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	defer sseResp.Body.Close()

	// Initialize
	resp, err := postJSON(postURL, &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("initialize POST status = %d, want 202", resp.StatusCode)
	}
	resp.Body.Close()

	// Read initialize response from SSE
	ev, err := readSSEEvent(reader)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Event != "message" {
		t.Fatalf("expected message event, got %q", ev.Event)
	}
	var initResp core.Response
	if err := json.Unmarshal([]byte(ev.Data), &initResp); err != nil {
		t.Fatalf("unmarshal init response: %v", err)
	}
	if initResp.Error != nil {
		t.Fatalf("init error: %s", initResp.Error.Message)
	}

	// Send notifications/initialized
	resp, err = postJSON(postURL, &core.Request{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("initialized notification status = %d, want 204", resp.StatusCode)
	}
	resp.Body.Close()

	// Call tool
	resp, err = postJSON(postURL, &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`2`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"echo","arguments":{"message":"hello"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("tool call POST status = %d, want 202", resp.StatusCode)
	}
	resp.Body.Close()

	// Read tool response from SSE
	ev, err = readSSEEvent(reader)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Event != "message" {
		t.Fatalf("expected message event, got %q", ev.Event)
	}
	var toolResp core.Response
	if err := json.Unmarshal([]byte(ev.Data), &toolResp); err != nil {
		t.Fatalf("unmarshal tool response: %v", err)
	}
	if toolResp.Error != nil {
		t.Fatalf("tool error: %s", toolResp.Error.Message)
	}
	var result core.ToolResult
	if err := json.Unmarshal(toolResp.Result, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Content) == 0 || result.Content[0].Text != "echo: hello" {
		t.Errorf("tool result = %+v, want echo: hello", result)
	}
}

// TestSSESessionNotFound verifies that POSTing to a nonexistent session returns 410 Gone.
func TestSSESessionNotFound(t *testing.T) {
	ts, _ := testMCPServer()
	defer ts.Close()

	resp, err := postJSON(ts.URL+"/mcp/message?sessionId=nonexistent", &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "ping",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusGone {
		t.Errorf("status = %d, want 410", resp.StatusCode)
	}
}

// TestSSENotification verifies that POSTing a JSON-RPC notification (no ID) returns
// HTTP 204 No core.Content, since notifications have no response to push on the SSE stream.
func TestSSENotification(t *testing.T) {
	ts, _ := testMCPServer()
	defer ts.Close()

	sseResp, _, postURL, err := connectSSE(ts, "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	defer sseResp.Body.Close()

	// Send initialize first (required before notifications/initialized)
	resp, err := postJSON(postURL, &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Send notification (no ID = notification)
	resp, err = postJSON(postURL, &core.Request{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}
}

// TestSSEAuthRequired verifies that when bearer token auth is configured,
// both the SSE GET endpoint and POST message endpoint require auth.
func TestSSEAuthRequired(t *testing.T) {
	srv := NewServer(
		core.ServerInfo{Name: "test", Version: "0.1.0"},
		WithBearerToken("secret"),
	)
	srv.RegisterTool(
		core.ToolDef{Name: "echo", Description: "echo"},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("ok"), nil
		},
	)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// SSE GET without auth should return 401
	sseURL := ts.URL + "/mcp/sse"
	resp, err := http.Get(sseURL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("SSE GET without auth: status = %d, want 401", resp.StatusCode)
	}

	// SSE GET with auth should succeed (connect and get endpoint event)
	sseResp, _, postURL, err := connectSSEWithAuth(ts, "/mcp", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer sseResp.Body.Close()

	// POST without auth should return 401
	noAuthResp, err := postJSON(postURL, &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "ping",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer noAuthResp.Body.Close()

	if noAuthResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST without auth: status = %d, want 401", noAuthResp.StatusCode)
	}
}

// TestSSEMaxSessions verifies that when maxSessions is set, exceeding the limit
// returns 503 Service Unavailable on the SSE endpoint.
func TestSSEMaxSessions(t *testing.T) {
	ts, _ := testMCPServer(WithMaxSessions(1))
	defer ts.Close()

	// First connection succeeds
	sseResp1, _, _, err := connectSSE(ts, "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	defer sseResp1.Body.Close()

	// Second connection should fail
	resp, err := http.Get(ts.URL + "/mcp/sse")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("second SSE status = %d, want 503", resp.StatusCode)
	}
}

// TestSSEClientDisconnect verifies that after a client disconnects its SSE stream,
// subsequent POSTs to that session return 410 Gone.
func TestSSEClientDisconnect(t *testing.T) {
	ts, _ := testMCPServer()
	defer ts.Close()

	sseResp, _, postURL, err := connectSSE(ts, "/mcp")
	if err != nil {
		t.Fatal(err)
	}

	// Close the SSE connection
	sseResp.Body.Close()

	// Give the server a moment to clean up
	time.Sleep(50 * time.Millisecond)

	// POST should now get 410
	resp, err := postJSON(postURL, &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "ping",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusGone {
		t.Errorf("status after disconnect = %d, want 410", resp.StatusCode)
	}
}

// TestSSEParseError verifies that posting malformed JSON pushes a JSON-RPC parse
// error response on the SSE stream and returns HTTP 202.
func TestSSEParseError(t *testing.T) {
	ts, _ := testMCPServer()
	defer ts.Close()

	sseResp, reader, postURL, err := connectSSE(ts, "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	defer sseResp.Body.Close()

	// POST malformed JSON
	resp, err := http.Post(postURL, "application/json", strings.NewReader("not json"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	resp.Body.Close()

	// Read parse error from SSE
	ev, err := readSSEEvent(reader)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Event != "message" {
		t.Fatalf("expected message event, got %q", ev.Event)
	}
	var errResp core.Response
	if err := json.Unmarshal([]byte(ev.Data), &errResp); err != nil {
		t.Fatal(err)
	}
	if errResp.Error == nil {
		t.Fatal("expected JSON-RPC error")
	}
	if errResp.Error.Code != core.ErrCodeParse {
		t.Errorf("error code = %d, want %d", errResp.Error.Code, core.ErrCodeParse)
	}
}

// TestSSECustomPrefix verifies that WithPrefix changes the URL paths for both
// the SSE and message endpoints.
func TestSSECustomPrefix(t *testing.T) {
	ts, _ := testMCPServer(WithPrefix("/custom"))
	defer ts.Close()

	sseResp, _, postURL, err := connectSSE(ts, "/custom")
	if err != nil {
		t.Fatal(err)
	}
	defer sseResp.Body.Close()

	if !strings.Contains(postURL, "/custom/message?sessionId=") {
		t.Errorf("endpoint URL = %q, want /custom/message path", postURL)
	}
}

// TestSSEPublicURL verifies that WithPublicURL overrides the host in the endpoint
// event's POST URL, allowing the server to work behind a reverse proxy.
func TestSSEPublicURL(t *testing.T) {
	ts, _ := testMCPServer(WithPublicURL("https://proxy.example.com"))
	defer ts.Close()

	sseResp, _, postURL, err := connectSSE(ts, "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	defer sseResp.Body.Close()

	if !strings.HasPrefix(postURL, "https://proxy.example.com/mcp/message?sessionId=") {
		t.Errorf("endpoint URL = %q, want https://proxy.example.com prefix", postURL)
	}
}

// TestSSELoggingNotification verifies the full MCP logging lifecycle over SSE:
// 1. Connect SSE and complete the init handshake
// 2. Set log level via logging/setLevel
// 3. Call a tool that emits a log notification via core.EmitLog
// 4. Verify the notifications/message event arrives on the SSE stream before the tool result
//
// This exercises the complete notification pipeline: core.EmitLog → core.NotifyFunc → hub.SendEvent → SSE stream.
func TestSSELoggingNotification(t *testing.T) {
	srv := NewServer(core.ServerInfo{Name: "test-logging", Version: "0.1.0"})

	// Register a tool that emits a log notification
	srv.RegisterTool(
		core.ToolDef{
			Name:        "log_emitter",
			Description: "Emits a log notification then returns a result",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			core.EmitLog(ctx, core.LogInfo, "test-logger", "hello from tool")
			return core.TextResult("done"), nil
		},
	)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Connect SSE
	sseResp, reader, postURL, err := connectSSE(ts, "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	defer sseResp.Body.Close()

	// Initialize
	resp, err := postJSON(postURL, &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	// Consume init response from SSE
	if _, err := readSSEEvent(reader); err != nil {
		t.Fatal(err)
	}

	// Send notifications/initialized
	resp, err = postJSON(postURL, &core.Request{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Set log level to debug (accept everything)
	resp, err = postJSON(postURL, &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`2`),
		Method:  "logging/setLevel",
		Params:  json.RawMessage(`{"level":"debug"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	// Consume logging/setLevel response from SSE
	if _, err := readSSEEvent(reader); err != nil {
		t.Fatal(err)
	}

	// Call the tool that emits a log
	resp, err = postJSON(postURL, &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`3`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"log_emitter","arguments":{}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Read the log notification from SSE (should arrive before or with the tool result)
	// We expect two events: the notifications/message and the tool result.
	var logNotification, toolResult *sseEvent
	for i := 0; i < 2; i++ {
		ev, err := readSSEEvent(reader)
		if err != nil {
			t.Fatalf("reading event %d: %v", i, err)
		}

		// Determine if this is a notification (no id) or a response (has id)
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(ev.Data), &obj); err != nil {
			t.Fatalf("event %d: unmarshal: %v", i, err)
		}
		if _, hasMethod := obj["method"]; hasMethod {
			logNotification = &ev
		} else {
			toolResult = &ev
		}
	}

	if logNotification == nil {
		t.Fatal("did not receive log notification")
	}
	if toolResult == nil {
		t.Fatal("did not receive tool result")
	}

	// Verify the log notification structure
	var notif struct {
		JSONRPC string     `json:"jsonrpc"`
		Method  string     `json:"method"`
		Params  core.LogMessage `json:"params"`
	}
	if err := json.Unmarshal([]byte(logNotification.Data), &notif); err != nil {
		t.Fatalf("unmarshal notification: %v", err)
	}
	if notif.JSONRPC != "2.0" {
		t.Errorf("jsonrpc = %q, want 2.0", notif.JSONRPC)
	}
	if notif.Method != "notifications/message" {
		t.Errorf("method = %q, want notifications/message", notif.Method)
	}
	if notif.Params.Level != "info" {
		t.Errorf("level = %q, want info", notif.Params.Level)
	}
	if notif.Params.Logger != "test-logger" {
		t.Errorf("logger = %q, want test-logger", notif.Params.Logger)
	}

	// Verify the tool result
	var toolResp core.Response
	if err := json.Unmarshal([]byte(toolResult.Data), &toolResp); err != nil {
		t.Fatalf("unmarshal tool response: %v", err)
	}
	if toolResp.Error != nil {
		t.Fatalf("tool error: %s", toolResp.Error.Message)
	}
}

// TestSSELoggingFilteredByLevel verifies that log notifications are not sent
// when the message level is below the session's minimum. The tool emits a debug
// message but the session level is set to error, so no notification should arrive.
func TestSSELoggingFilteredByLevel(t *testing.T) {
	srv := NewServer(core.ServerInfo{Name: "test-filter", Version: "0.1.0"})

	srv.RegisterTool(
		core.ToolDef{
			Name:        "debug_logger",
			Description: "Emits a debug log",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			core.EmitLog(ctx, core.LogDebug, "test", "debug msg")
			return core.TextResult("ok"), nil
		},
	)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	sseResp, reader, postURL, err := connectSSE(ts, "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	defer sseResp.Body.Close()

	// Init handshake
	resp, _ := postJSON(postURL, &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "initialize",
		Params: json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`),
	})
	resp.Body.Close()
	readSSEEvent(reader) // consume init response

	resp, _ = postJSON(postURL, &core.Request{JSONRPC: "2.0", Method: "notifications/initialized"})
	resp.Body.Close()

	// Set level to error (high threshold)
	resp, _ = postJSON(postURL, &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "logging/setLevel",
		Params: json.RawMessage(`{"level":"error"}`),
	})
	resp.Body.Close()
	readSSEEvent(reader) // consume setLevel response

	// Call tool that emits debug (should be filtered)
	resp, _ = postJSON(postURL, &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`3`), Method: "tools/call",
		Params: json.RawMessage(`{"name":"debug_logger","arguments":{}}`),
	})
	resp.Body.Close()

	// Should only get the tool result, no notification
	ev, err := readSSEEvent(reader)
	if err != nil {
		t.Fatal(err)
	}

	// Verify this is the tool result (has id), not a notification
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(ev.Data), &obj); err != nil {
		t.Fatal(err)
	}
	if _, hasMethod := obj["method"]; hasMethod {
		t.Error("received a notification despite debug level being filtered")
	}
	if _, hasID := obj["id"]; !hasID {
		t.Error("expected tool result with id")
	}
}

// TestSSEProgressNotification verifies the full MCP progress notification lifecycle over SSE:
// 1. Connect SSE and complete the init handshake
// 2. Call a tool with _meta.progressToken that emits progress notifications via core.EmitProgress
// 3. Verify notifications/progress events arrive on the SSE stream with the correct token
//
// This exercises: _meta.progressToken extraction → core.EmitProgress → core.NotifyFunc → hub.SendEvent → SSE stream.
func TestSSEProgressNotification(t *testing.T) {
	srv := NewServer(core.ServerInfo{Name: "test-progress", Version: "0.1.0"})

	srv.RegisterTool(
		core.ToolDef{
			Name:        "progress_tool",
			Description: "Emits progress notifications then returns a result",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			core.EmitProgress(ctx, req.ProgressToken, 0, 100, "start")
			core.EmitProgress(ctx, req.ProgressToken, 50, 100, "mid")
			core.EmitProgress(ctx, req.ProgressToken, 100, 100, "done")
			return core.TextResult("complete"), nil
		},
	)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	sseResp, reader, postURL, err := connectSSE(ts, "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	defer sseResp.Body.Close()

	// Initialize
	resp, _ := postJSON(postURL, &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "initialize",
		Params: json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`),
	})
	resp.Body.Close()
	readSSEEvent(reader)

	resp, _ = postJSON(postURL, &core.Request{JSONRPC: "2.0", Method: "notifications/initialized"})
	resp.Body.Close()

	// Call tool with progress token
	resp, _ = postJSON(postURL, &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "tools/call",
		Params: json.RawMessage(`{"name":"progress_tool","arguments":{},"_meta":{"progressToken":"test-token"}}`),
	})
	resp.Body.Close()

	// Read 4 events: 3 progress notifications + 1 tool result
	var notifications []core.ProgressNotification
	var toolResult *sseEvent
	for i := 0; i < 4; i++ {
		ev, err := readSSEEvent(reader)
		if err != nil {
			t.Fatalf("reading event %d: %v", i, err)
		}

		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(ev.Data), &obj); err != nil {
			t.Fatalf("event %d: unmarshal: %v", i, err)
		}

		if methodRaw, ok := obj["method"]; ok {
			var method string
			json.Unmarshal(methodRaw, &method)
			if method == "notifications/progress" {
				var notif struct {
					Params core.ProgressNotification `json:"params"`
				}
				json.Unmarshal([]byte(ev.Data), &notif)
				notifications = append(notifications, notif.Params)
			}
		} else {
			toolResult = &ev
		}
	}

	if len(notifications) != 3 {
		t.Fatalf("got %d progress notifications, want 3", len(notifications))
	}
	if toolResult == nil {
		t.Fatal("did not receive tool result")
	}

	// Verify progress values are monotonically increasing
	wantProgress := []float64{0, 50, 100}
	for i, want := range wantProgress {
		if notifications[i].Progress != want {
			t.Errorf("notification[%d].Progress = %v, want %v", i, notifications[i].Progress, want)
		}
		if notifications[i].ProgressToken != "test-token" {
			t.Errorf("notification[%d].ProgressToken = %v, want test-token", i, notifications[i].ProgressToken)
		}
	}
}
