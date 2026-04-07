package mcpkit

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testStreamableServer creates an httptest.Server with a Streamable HTTP MCP server
// that has an echo tool. Returns the server and its base URL.
func testStreamableServer(opts ...TransportOption) *httptest.Server {
	srv := NewServer(ServerInfo{Name: "test-streamable", Version: "0.1.0"})
	srv.RegisterTool(
		ToolDef{
			Name:        "echo",
			Description: "Echoes the input",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"message": map[string]any{"type": "string"},
				},
			},
		},
		func(ctx context.Context, req ToolRequest) (ToolResult, error) {
			var args struct {
				Message string `json:"message"`
			}
			req.Bind(&args)
			return TextResult("echo: " + args.Message), nil
		},
	)
	allOpts := append([]TransportOption{WithStreamableHTTP(true), WithSSE(false)}, opts...)
	return httptest.NewServer(srv.Handler(allOpts...))
}

// streamablePost sends a JSON-RPC request to the streamable endpoint and returns
// the HTTP response. Adds Mcp-Session-Id header if sessionID is non-empty.
// streamablePost sends a JSON-RPC request expecting a synchronous JSON response.
// Uses Accept: application/json only — no SSE streaming.
// streamablePost sends a Streamable HTTP POST requesting JSON-only responses.
// Use this for tests that verify the synchronous JSON path.
// For SSE streaming tests, use streamablePostSSE.
func streamablePost(url, sessionID string, body any) (*http.Response, error) {
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if sessionID != "" {
		req.Header.Set(mcpSessionIDHeader, sessionID)
	}
	return http.DefaultClient.Do(req)
}

// streamableInit performs the initialize handshake and returns the session ID.
func streamableInit(t *testing.T, url string) string {
	t.Helper()
	resp, err := streamablePost(url+"/mcp", "", &Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`),
	})
	if err != nil {
		t.Fatalf("initialize POST failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("initialize status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	sessionID := resp.Header.Get(mcpSessionIDHeader)
	if sessionID == "" {
		t.Fatal("no Mcp-Session-Id header in initialize response")
	}

	// Send initialized notification
	resp2, err := streamablePost(url+"/mcp", sessionID, &Request{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	})
	if err != nil {
		t.Fatalf("initialized notification failed: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusAccepted {
		t.Fatalf("initialized notification status = %d, want 202", resp2.StatusCode)
	}

	return sessionID
}

// TestStreamableInitAndToolCall verifies the full MCP lifecycle over Streamable HTTP:
// POST initialize → get session ID from header → POST notifications/initialized (202) →
// POST tools/call → get JSON response with tool result in body.
func TestStreamableInitAndToolCall(t *testing.T) {
	ts := testStreamableServer()
	defer ts.Close()

	sessionID := streamableInit(t, ts.URL)

	// Call tool
	resp, err := streamablePost(ts.URL+"/mcp", sessionID, &Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`2`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"echo","arguments":{"message":"hello"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tools/call status = %d, want 200", resp.StatusCode)
	}

	var rpcResp Response
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if rpcResp.Error != nil {
		t.Fatalf("JSON-RPC error: %s", rpcResp.Error.Message)
	}

	var result ToolResult
	json.Unmarshal(rpcResp.Result, &result)
	if len(result.Content) == 0 || result.Content[0].Text != "echo: hello" {
		t.Errorf("tool result = %+v, want echo: hello", result)
	}
}

// TestStreamableInitReturnsSessionID verifies that the initialize response includes
// a Mcp-Session-Id header with a non-empty, cryptographically random session ID.
func TestStreamableInitReturnsSessionID(t *testing.T) {
	ts := testStreamableServer()
	defer ts.Close()

	resp, err := streamablePost(ts.URL+"/mcp", "", &Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	sessionID := resp.Header.Get(mcpSessionIDHeader)
	if sessionID == "" {
		t.Fatal("missing Mcp-Session-Id header")
	}
	if len(sessionID) != 32 {
		t.Errorf("session ID length = %d, want 32 (hex-encoded 16 bytes)", len(sessionID))
	}

	// Verify response is valid JSON-RPC
	var rpcResp Response
	json.NewDecoder(resp.Body).Decode(&rpcResp)
	if rpcResp.Error != nil {
		t.Fatalf("init error: %s", rpcResp.Error.Message)
	}
	var result map[string]any
	json.Unmarshal(rpcResp.Result, &result)
	if result["protocolVersion"] != "2024-11-05" {
		t.Errorf("protocolVersion = %v, want 2024-11-05", result["protocolVersion"])
	}
}

// TestStreamableMissingSessionID verifies that non-initialize POST requests
// without a Mcp-Session-Id header return 400 Bad Request.
func TestStreamableMissingSessionID(t *testing.T) {
	ts := testStreamableServer()
	defer ts.Close()

	resp, err := streamablePost(ts.URL+"/mcp", "", &Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/list",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// TestStreamableUnknownSessionID verifies that POST requests with an unknown
// session ID return 404 Not Found, as the spec requires for expired sessions.
func TestStreamableUnknownSessionID(t *testing.T) {
	ts := testStreamableServer()
	defer ts.Close()

	resp, err := streamablePost(ts.URL+"/mcp", "nonexistent-session-id", &Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/list",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// TestStreamableNotification verifies that POST notifications (no ID) with a
// valid session return 202 Accepted with no body.
func TestStreamableNotification(t *testing.T) {
	ts := testStreamableServer()
	defer ts.Close()

	sessionID := streamableInit(t, ts.URL)

	resp, err := streamablePost(ts.URL+"/mcp", sessionID, &Request{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("status = %d, want 202", resp.StatusCode)
	}
}

// TestStreamableDeleteSession verifies that DELETE with a valid session ID
// terminates the session (200), and subsequent requests to that session
// return 404 Not Found.
func TestStreamableDeleteSession(t *testing.T) {
	ts := testStreamableServer()
	defer ts.Close()

	sessionID := streamableInit(t, ts.URL)

	// DELETE the session
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/mcp", nil)
	req.Header.Set(mcpSessionIDHeader, sessionID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE status = %d, want 200", resp.StatusCode)
	}

	// Subsequent POST should get 404
	resp2, err := streamablePost(ts.URL+"/mcp", sessionID, &Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "ping",
	})
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()

	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("post-delete status = %d, want 404", resp2.StatusCode)
	}
}

// TestStreamableDeleteMissingSession verifies that DELETE with an unknown
// session ID returns 404 Not Found.
func TestStreamableDeleteMissingSession(t *testing.T) {
	ts := testStreamableServer()
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/mcp", nil)
	req.Header.Set(mcpSessionIDHeader, "nonexistent")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// TestStreamableDeleteNoSessionHeader verifies that DELETE without a
// Mcp-Session-Id header returns 400 Bad Request.
func TestStreamableDeleteNoSessionHeader(t *testing.T) {
	ts := testStreamableServer()
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/mcp", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// TestStreamableGetSSE_RequiresSession verifies that GET /mcp without a
// Mcp-Session-Id header returns 400, since the GET SSE stream only works
// on existing sessions.
func TestStreamableGetSSE_RequiresSession(t *testing.T) {
	ts := testStreamableServer()
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (missing session)", resp.StatusCode)
	}
}

// TestStreamableParseError verifies that posting malformed JSON returns a
// JSON-RPC parse error in the response body (not an HTTP error page).
func TestStreamableParseError(t *testing.T) {
	ts := testStreamableServer()
	defer ts.Close()

	sessionID := streamableInit(t, ts.URL)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set(mcpSessionIDHeader, sessionID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var rpcResp Response
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if rpcResp.Error == nil {
		t.Fatal("expected JSON-RPC error")
	}
	if rpcResp.Error.Code != ErrCodeParse {
		t.Errorf("error code = %d, want %d", rpcResp.Error.Code, ErrCodeParse)
	}
}

// TestStreamableAuthRequired verifies that when bearer token auth is configured,
// POST requests without an Authorization header return 401 Unauthorized.
func TestStreamableAuthRequired(t *testing.T) {
	srv := NewServer(
		ServerInfo{Name: "test", Version: "0.1.0"},
		WithBearerToken("secret"),
	)
	srv.RegisterTool(
		ToolDef{Name: "echo", Description: "echo"},
		func(ctx context.Context, req ToolRequest) (ToolResult, error) {
			return TextResult("ok"), nil
		},
	)
	ts := httptest.NewServer(srv.Handler(WithStreamableHTTP(true), WithSSE(false)))
	defer ts.Close()

	resp, err := streamablePost(ts.URL+"/mcp", "", &Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// TestStreamableMaxSessions verifies that when maxSessions is set, attempting
// to create more sessions than the limit returns 503 Service Unavailable.
func TestStreamableMaxSessions(t *testing.T) {
	ts := testStreamableServer(WithMaxSessions(1))
	defer ts.Close()

	// First session succeeds
	_ = streamableInit(t, ts.URL)

	// Second should fail
	resp, err := streamablePost(ts.URL+"/mcp", "", &Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("second init status = %d, want 503", resp.StatusCode)
	}
}

// TestStreamableProtocolVersionHeader verifies that after initialization, the
// server rejects requests with a mismatched MCP-Protocol-Version header (400)
// and accepts requests with the correct version.
func TestStreamableProtocolVersionHeader(t *testing.T) {
	ts := testStreamableServer()
	defer ts.Close()

	sessionID := streamableInit(t, ts.URL)

	// Request with wrong protocol version → 400
	raw, _ := json.Marshal(&Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "ping",
	})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set(mcpSessionIDHeader, sessionID)
	req.Header.Set(mcpProtocolVersionHeader, "1999-01-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("wrong version status = %d, want 400", resp.StatusCode)
	}

	// Request with correct protocol version → 200
	req2, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", bytes.NewReader(raw))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Accept", "application/json, text/event-stream")
	req2.Header.Set(mcpSessionIDHeader, sessionID)
	req2.Header.Set(mcpProtocolVersionHeader, "2024-11-05")

	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Errorf("correct version status = %d, want 200", resp2.StatusCode)
	}
}

// TestStreamableCustomPrefix verifies that WithPrefix changes the URL path
// for the Streamable HTTP endpoint.
func TestStreamableCustomPrefix(t *testing.T) {
	ts := testStreamableServer(WithPrefix("/custom"))
	defer ts.Close()

	resp, err := streamablePost(ts.URL+"/custom", "", &Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if resp.Header.Get(mcpSessionIDHeader) == "" {
		t.Error("missing Mcp-Session-Id header")
	}
}

// testStreamableServerWithLogging creates a test server that has a tool which emits
// log notifications during execution, for testing SSE streaming responses.
func testStreamableServerWithLogging(opts ...TransportOption) *httptest.Server {
	srv := NewServer(ServerInfo{Name: "test-streamable-sse", Version: "0.1.0"})
	srv.RegisterTool(
		ToolDef{Name: "echo", Description: "Echoes input", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, req ToolRequest) (ToolResult, error) {
			return TextResult("ok"), nil
		},
	)
	srv.RegisterTool(
		ToolDef{Name: "log_tool", Description: "Emits logs", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, req ToolRequest) (ToolResult, error) {
			EmitLog(ctx, LogInfo, "test", "step one")
			EmitLog(ctx, LogInfo, "test", "step two")
			return TextResult("done"), nil
		},
	)
	allOpts := append([]TransportOption{WithStreamableHTTP(true), WithSSE(false)}, opts...)
	return httptest.NewServer(srv.Handler(allOpts...))
}

// streamableInitWithLogging initializes a session and enables logging on it.
func streamableInitWithLogging(t *testing.T, url string) string {
	t.Helper()
	sessionID := streamableInit(t, url)

	// Enable logging at debug level
	resp, err := streamablePost(url+"/mcp", sessionID, &Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`99`),
		Method:  "logging/setLevel",
		Params:  json.RawMessage(`{"level":"debug"}`),
	})
	if err != nil {
		t.Fatalf("logging/setLevel failed: %v", err)
	}
	resp.Body.Close()
	return sessionID
}

// streamablePostSSE sends a JSON-RPC request with Accept: text/event-stream
// and returns the raw response for SSE parsing.
// streamablePostSSE sends a Streamable HTTP POST requesting SSE streaming.
// Per MCP spec: when Accept includes text/event-stream, the server may stream
// notifications before the final response.
func streamablePostSSE(url, sessionID string, body any) (*http.Response, error) {
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		req.Header.Set(mcpSessionIDHeader, sessionID)
	}
	return http.DefaultClient.Do(req)
}

// readSSEEvents reads all SSE events from a response body until EOF.
func readSSEEvents(t *testing.T, body io.Reader) []sseEvent {
	t.Helper()
	var events []sseEvent
	scanner := bufio.NewScanner(body)
	var event, data string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if data != "" || event != "" {
				events = append(events, sseEvent{Event: event, Data: data})
				event, data = "", ""
			}
			continue
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
	}
	return events
}

// TestStreamableSSEResponse verifies that when a tool emits log notifications during
// execution and the client sends Accept: text/event-stream, the response is an SSE
// stream containing notification events followed by the JSON-RPC response.
func TestStreamableSSEResponse(t *testing.T) {
	ts := testStreamableServerWithLogging()
	defer ts.Close()

	sessionID := streamableInitWithLogging(t, ts.URL)

	resp, err := streamablePostSSE(ts.URL+"/mcp", sessionID, &Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"log_tool","arguments":{}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	events := readSSEEvents(t, resp.Body)
	if len(events) < 3 {
		t.Fatalf("got %d events, want at least 3 (2 notifications + 1 response)", len(events))
	}

	// First two events should be log notifications
	for i := 0; i < 2; i++ {
		if !strings.Contains(events[i].Data, "notifications/message") {
			t.Errorf("event[%d] = %q, want notifications/message", i, events[i].Data)
		}
	}

	// Last event should be the JSON-RPC response
	last := events[len(events)-1]
	if !strings.Contains(last.Data, `"id":1`) {
		t.Errorf("last event missing response id: %q", last.Data)
	}
	if !strings.Contains(last.Data, `"result"`) {
		t.Errorf("last event missing result: %q", last.Data)
	}
}

// TestStreamableSSEFallback verifies that when the client does NOT include
// Accept: text/event-stream, the response is synchronous JSON (not SSE),
// preserving backward compatibility.
func TestStreamableSSEFallback(t *testing.T) {
	ts := testStreamableServerWithLogging()
	defer ts.Close()

	sessionID := streamableInitWithLogging(t, ts.URL)

	// POST with Accept: application/json only (no text/event-stream) — should get JSON
	body, _ := json.Marshal(&Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"log_tool","arguments":{}}`),
	})
	httpReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json") // intentionally JSON-only to test fallback path
	httpReq.Header.Set(mcpSessionIDHeader, sessionID)
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}

	var rpcResp Response
	json.NewDecoder(resp.Body).Decode(&rpcResp)
	if rpcResp.Error != nil {
		t.Fatalf("JSON-RPC error: %s", rpcResp.Error.Message)
	}
}

// TestStreamableSSENotificationOrder verifies that notifications appear before
// the JSON-RPC response in the SSE event stream.
func TestStreamableSSENotificationOrder(t *testing.T) {
	ts := testStreamableServerWithLogging()
	defer ts.Close()

	sessionID := streamableInitWithLogging(t, ts.URL)

	resp, err := streamablePostSSE(ts.URL+"/mcp", sessionID, &Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"log_tool","arguments":{}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	events := readSSEEvents(t, resp.Body)
	// Find the response event (has "id" field)
	responseIdx := -1
	for i, ev := range events {
		if strings.Contains(ev.Data, `"id":1`) && strings.Contains(ev.Data, `"result"`) {
			responseIdx = i
			break
		}
	}
	if responseIdx < 0 {
		t.Fatal("no response event found in SSE stream")
	}

	// All events before the response should be notifications
	for i := 0; i < responseIdx; i++ {
		if !strings.Contains(events[i].Data, "notifications/") {
			t.Errorf("event[%d] before response is not a notification: %q", i, events[i].Data)
		}
	}

	// Response should be the last event
	if responseIdx != len(events)-1 {
		t.Errorf("response at index %d, but %d events total — response should be last", responseIdx, len(events))
	}
}

// TestStreamableSSENoNotifications verifies that when a tool doesn't emit any
// notifications, the SSE stream still works — containing only the response event.
func TestStreamableSSENoNotifications(t *testing.T) {
	ts := testStreamableServerWithLogging()
	defer ts.Close()

	sessionID := streamableInitWithLogging(t, ts.URL)

	// Call echo tool which doesn't emit logs
	resp, err := streamablePostSSE(ts.URL+"/mcp", sessionID, &Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"echo","arguments":{}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	events := readSSEEvents(t, resp.Body)
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 (response only)", len(events))
	}
	if !strings.Contains(events[0].Data, `"result"`) {
		t.Errorf("event missing result: %q", events[0].Data)
	}
}

// TestStreamableDNSRebindingRejectsInvalidOrigin verifies that requests with
// a non-localhost Origin header are rejected with 403 Forbidden, preventing
// DNS rebinding attacks per the MCP spec.
func TestStreamableDNSRebindingRejectsInvalidOrigin(t *testing.T) {
	ts := testStreamableServer()
	defer ts.Close()

	body, _ := json.Marshal(&Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`),
	})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Origin", "http://evil.example.com")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for non-localhost Origin", resp.StatusCode)
	}
}

// TestStreamableDNSRebindingAcceptsLocalhost verifies that requests with
// localhost Origin headers are accepted normally.
func TestStreamableDNSRebindingAcceptsLocalhost(t *testing.T) {
	ts := testStreamableServer()
	defer ts.Close()

	for _, origin := range []string{"http://localhost", "http://localhost:8787", "http://127.0.0.1:9999", "http://[::1]:3000"} {
		t.Run(origin, func(t *testing.T) {
			body, _ := json.Marshal(&Request{
				JSONRPC: "2.0",
				ID:      json.RawMessage(`1`),
				Method:  "initialize",
				Params:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`),
			})
			req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			req.Header.Set("Origin", origin)

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()

			if resp.StatusCode == http.StatusForbidden {
				t.Errorf("status = 403 for localhost Origin %q, should be accepted", origin)
			}
		})
	}
}

