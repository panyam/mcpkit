package mcpkit

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// sseEvent represents a parsed SSE event from the stream.
type sseEvent struct {
	Event string
	Data  string
}

// readSSEEvent reads the next SSE event from a bufio.Reader.
// It skips keepalive comments and returns the event type and data.
// Returns an error if the stream ends before a complete event is read.
func readSSEEvent(r *bufio.Reader) (sseEvent, error) {
	var event, data string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return sseEvent{}, fmt.Errorf("reading SSE: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")

		if line == "" {
			// Empty line = end of event
			if data != "" || event != "" {
				return sseEvent{Event: event, Data: data}, nil
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			// Comment (keepalive), skip
			continue
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
	}
}

// testMCPServer creates an httptest.Server with an MCP server that has an echo tool.
func testMCPServer(opts ...TransportOption) (*httptest.Server, *Server) {
	srv := NewServer(ServerInfo{Name: "test-sse", Version: "0.1.0"})
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
	handler := srv.Handler(opts...)
	ts := httptest.NewServer(handler)
	return ts, srv
}

// connectSSE opens an SSE connection to the test server and reads the endpoint event.
// Returns the SSE response (keep open for reading more events), the POST URL from the
// endpoint event, and any error.
func connectSSE(ts *httptest.Server, prefix string) (*http.Response, string, error) {
	resp, err := http.Get(ts.URL + prefix + "/sse")
	if err != nil {
		return nil, "", fmt.Errorf("GET /sse: %w", err)
	}

	reader := bufio.NewReader(resp.Body)
	ev, err := readSSEEvent(reader)
	if err != nil {
		resp.Body.Close()
		return nil, "", fmt.Errorf("reading endpoint event: %w", err)
	}
	if ev.Event != "endpoint" {
		resp.Body.Close()
		return nil, "", fmt.Errorf("expected endpoint event, got %q", ev.Event)
	}

	// The endpoint URL comes through JSONCodec, so it's a JSON string — unquote it.
	var postURL string
	if err := json.Unmarshal([]byte(ev.Data), &postURL); err != nil {
		resp.Body.Close()
		return nil, "", fmt.Errorf("parsing endpoint URL %q: %w", ev.Data, err)
	}

	return resp, postURL, nil
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

	sseResp, postURL, err := connectSSE(ts, "/mcp")
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

	sseResp, postURL, err := connectSSE(ts, "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	defer sseResp.Body.Close()
	reader := bufio.NewReader(sseResp.Body)

	// Initialize
	resp, err := postJSON(postURL, &Request{
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
	var initResp Response
	if err := json.Unmarshal([]byte(ev.Data), &initResp); err != nil {
		t.Fatalf("unmarshal init response: %v", err)
	}
	if initResp.Error != nil {
		t.Fatalf("init error: %s", initResp.Error.Message)
	}

	// Send notifications/initialized
	resp, err = postJSON(postURL, &Request{
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
	resp, err = postJSON(postURL, &Request{
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
	var toolResp Response
	if err := json.Unmarshal([]byte(ev.Data), &toolResp); err != nil {
		t.Fatalf("unmarshal tool response: %v", err)
	}
	if toolResp.Error != nil {
		t.Fatalf("tool error: %s", toolResp.Error.Message)
	}
	var result ToolResult
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

	resp, err := postJSON(ts.URL+"/mcp/message?sessionId=nonexistent", &Request{
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
// HTTP 204 No Content, since notifications have no response to push on the SSE stream.
func TestSSENotification(t *testing.T) {
	ts, _ := testMCPServer()
	defer ts.Close()

	sseResp, postURL, err := connectSSE(ts, "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	defer sseResp.Body.Close()

	// Send initialize first (required before notifications/initialized)
	resp, err := postJSON(postURL, &Request{
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
	resp, err = postJSON(postURL, &Request{
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
// POSTing without an Authorization header returns 401 Unauthorized.
func TestSSEAuthRequired(t *testing.T) {
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
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Connect SSE (no auth on SSE endpoint itself)
	sseResp, postURL, err := connectSSE(ts, "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	defer sseResp.Body.Close()

	// POST without auth
	resp, err := postJSON(postURL, &Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "ping",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// TestSSEMaxSessions verifies that when maxSessions is set, exceeding the limit
// returns 503 Service Unavailable on the SSE endpoint.
func TestSSEMaxSessions(t *testing.T) {
	ts, _ := testMCPServer(WithMaxSessions(1))
	defer ts.Close()

	// First connection succeeds
	sseResp1, _, err := connectSSE(ts, "/mcp")
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

	sseResp, postURL, err := connectSSE(ts, "/mcp")
	if err != nil {
		t.Fatal(err)
	}

	// Close the SSE connection
	sseResp.Body.Close()

	// Give the server a moment to clean up
	time.Sleep(50 * time.Millisecond)

	// POST should now get 410
	resp, err := postJSON(postURL, &Request{
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

	sseResp, postURL, err := connectSSE(ts, "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	defer sseResp.Body.Close()
	reader := bufio.NewReader(sseResp.Body)

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
	var errResp Response
	if err := json.Unmarshal([]byte(ev.Data), &errResp); err != nil {
		t.Fatal(err)
	}
	if errResp.Error == nil {
		t.Fatal("expected JSON-RPC error")
	}
	if errResp.Error.Code != ErrCodeParse {
		t.Errorf("error code = %d, want %d", errResp.Error.Code, ErrCodeParse)
	}
}

// TestSSECustomPrefix verifies that WithPrefix changes the URL paths for both
// the SSE and message endpoints.
func TestSSECustomPrefix(t *testing.T) {
	ts, _ := testMCPServer(WithPrefix("/custom"))
	defer ts.Close()

	sseResp, postURL, err := connectSSE(ts, "/custom")
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

	sseResp, postURL, err := connectSSE(ts, "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	defer sseResp.Body.Close()

	if !strings.HasPrefix(postURL, "https://proxy.example.com/mcp/message?sessionId=") {
		t.Errorf("endpoint URL = %q, want https://proxy.example.com prefix", postURL)
	}
}
