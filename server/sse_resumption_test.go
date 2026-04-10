package server

// SSE stream resumption tests (issue #174).
//
// These tests verify that SSE transport sessions can survive brief disconnects
// when WithSSEGracePeriod is configured, and that missed events are replayed
// via Last-Event-ID on reconnection. All tests are behavioral — they verify
// observable HTTP behavior rather than inspecting internal state.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	core "github.com/panyam/mcpkit/core"
	gohttp "github.com/panyam/servicekit/http"
)

// connectSSEWithParams opens an SSE connection with optional query params
// and headers. Used for reconnection tests where sessionId and Last-Event-ID
// need to be sent.
func connectSSEWithParams(t *testing.T, baseURL, prefix string, params map[string]string, headers map[string]string) (*http.Response, *gohttp.SSEEventReader, string, error) {
	t.Helper()
	sseURL := baseURL + prefix + "/sse"
	if len(params) > 0 {
		vals := url.Values{}
		for k, v := range params {
			vals.Set(k, v)
		}
		sseURL += "?" + vals.Encode()
	}
	req, err := http.NewRequest("GET", sseURL, nil)
	if err != nil {
		return nil, nil, "", err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, "", err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, nil, "", &gohttp.HTTPStatusError{StatusCode: resp.StatusCode}
	}
	reader := gohttp.NewSSEEventReader(resp.Body)
	ev, err := readSSEEvent(reader)
	if err != nil {
		resp.Body.Close()
		return nil, nil, "", err
	}
	if ev.Event != "endpoint" {
		resp.Body.Close()
		return nil, nil, "", fmt.Errorf("expected endpoint event, got %q: %s", ev.Event, ev.Data)
	}
	return resp, reader, ev.Data, nil
}

// extractSessionID parses the sessionId query param from an endpoint URL.
func extractSessionID(endpointURL string) string {
	u, err := url.Parse(endpointURL)
	if err != nil {
		return ""
	}
	return u.Query().Get("sessionId")
}

// initializeSSESession performs the MCP initialize handshake over an SSE
// connection by POSTing the initialize request and reading the response event.
func initializeSSESession(t *testing.T, postURL string, reader *gohttp.SSEEventReader) {
	t.Helper()
	resp, err := postJSON(postURL, &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "initialize",
		Params: json.RawMessage(`{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}`),
	})
	if err != nil {
		t.Fatalf("initialize POST failed: %v", err)
	}
	resp.Body.Close()
	// Read initialize response from SSE stream.
	readSSEEvent(reader)
	// Send initialized notification.
	resp, err = postJSON(postURL, &core.Request{
		JSONRPC: "2.0", Method: "notifications/initialized",
	})
	if err != nil {
		t.Fatalf("initialized notification failed: %v", err)
	}
	resp.Body.Close()
}

// TestSSESessionGracePeriod verifies that when WithSSEGracePeriod is configured,
// disconnecting an SSE connection does NOT destroy the session immediately. A
// reconnection within the grace period reuses the same session — the same
// Dispatcher with the same initialized state and tool registrations. The client
// proves session reuse by making a tool call after reconnection without
// re-initializing.
func TestSSESessionGracePeriod(t *testing.T) {
	ts, _ := testMCPServer(
		WithSSE(true),
		WithStreamableHTTP(false),
		WithSSEGracePeriod(5*time.Second),
	)
	defer ts.Close()

	// Connect and initialize.
	sseResp, reader, postURL, err := connectSSE(ts, "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	sessionID := extractSessionID(postURL)
	if sessionID == "" {
		t.Fatal("no sessionId in endpoint URL")
	}
	initializeSSESession(t, postURL, reader)

	// Disconnect (close the SSE stream).
	sseResp.Body.Close()
	time.Sleep(200 * time.Millisecond) // let OnClose fire

	// Reconnect with the same session ID.
	sseResp2, reader2, postURL2, err := connectSSEWithParams(t, ts.URL, "/mcp",
		map[string]string{"sessionId": sessionID}, nil)
	if err != nil {
		t.Fatalf("reconnect failed: %v", err)
	}
	defer sseResp2.Body.Close()

	// Verify the session ID is the same.
	newSessionID := extractSessionID(postURL2)
	if newSessionID != sessionID {
		t.Errorf("reconnected session ID = %q, want %q", newSessionID, sessionID)
	}

	// Make a tool call WITHOUT re-initializing — proves session state survived.
	postJSON(postURL2, &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`10`), Method: "tools/call",
		Params: json.RawMessage(`{"name":"echo","arguments":{"message":"after-reconnect"}}`),
	})
	ev, err := readSSEEvent(reader2)
	if err != nil {
		t.Fatalf("reading tool call response after reconnect: %v", err)
	}
	if !strings.Contains(ev.Data, "after-reconnect") {
		t.Errorf("tool call response = %s, want to contain 'after-reconnect'", ev.Data)
	}
}

// TestSSESessionGraceExpiry verifies that when the grace period expires without
// reconnection, the session is fully cleaned up. A POST to the old session ID
// returns 410 Gone.
func TestSSESessionGraceExpiry(t *testing.T) {
	ts, _ := testMCPServer(
		WithSSE(true),
		WithStreamableHTTP(false),
		WithSSEGracePeriod(200*time.Millisecond),
	)
	defer ts.Close()

	sseResp, reader, postURL, err := connectSSE(ts, "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	sessionID := extractSessionID(postURL)
	initializeSSESession(t, postURL, reader)

	// Disconnect.
	sseResp.Body.Close()
	time.Sleep(100 * time.Millisecond)

	// POST should still work during grace period.
	resp, _ := postJSON(postURL, &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`5`), Method: "tools/list",
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("during grace period: POST status = %d, want 202", resp.StatusCode)
	}
	resp.Body.Close()

	// Wait for grace period to expire.
	time.Sleep(300 * time.Millisecond)

	// POST should now return 410 Gone.
	resp2, _ := postJSON(postURL, &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`6`), Method: "tools/list",
	})
	if resp2.StatusCode != http.StatusGone {
		t.Errorf("after grace period: POST status = %d, want %d (Gone)", resp2.StatusCode, http.StatusGone)
	}
	resp2.Body.Close()

	// Attempting to reconnect via SSE should also get 410.
	sseURL := ts.URL + "/mcp/sse?sessionId=" + sessionID
	resp3, err := http.Get(sseURL)
	if err != nil {
		t.Fatal(err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusGone {
		t.Errorf("SSE reconnect after expiry: status = %d, want %d (Gone)", resp3.StatusCode, http.StatusGone)
	}
}

// TestSSELastEventIDReplay verifies that when a client reconnects with a
// Last-Event-ID header, the server replays missed events from the EventStore
// before starting live delivery. This enables seamless resumption after brief
// network interruptions without losing responses.
func TestSSELastEventIDReplay(t *testing.T) {
	store := gohttp.NewMemoryEventStore(100)
	ts, _ := testMCPServer(
		WithSSE(true),
		WithStreamableHTTP(false),
		WithSSEGracePeriod(5*time.Second),
		WithEventStore(store),
	)
	defer ts.Close()

	// Connect and initialize.
	sseResp, reader, postURL, err := connectSSE(ts, "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	sessionID := extractSessionID(postURL)
	initializeSSESession(t, postURL, reader)

	// Make a tool call to generate events with IDs.
	postJSON(postURL, &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`10`), Method: "tools/call",
		Params: json.RawMessage(`{"name":"echo","arguments":{"message":"stored-event"}}`),
	})
	readSSEEvent(reader) // consume tool response

	// Get the first stored event ID as our "already seen" anchor.
	events, _ := store.Replay(sessionID, "")
	if len(events) == 0 {
		t.Fatal("expected events in store after tool call")
	}
	firstEventID := events[0].ID
	totalEvents := len(events)

	// Disconnect.
	sseResp.Body.Close()
	time.Sleep(200 * time.Millisecond)

	// Reconnect with Last-Event-ID set to the first event.
	// The server should replay all events after firstEventID.
	sseResp2, reader2, _, err := connectSSEWithParams(t, ts.URL, "/mcp",
		map[string]string{"sessionId": sessionID},
		map[string]string{"Last-Event-ID": firstEventID})
	if err != nil {
		t.Fatalf("reconnect with Last-Event-ID failed: %v", err)
	}
	defer sseResp2.Body.Close()

	// Count replayed events. We should get (totalEvents - 1) replayed events
	// (everything after firstEventID).
	expectedReplays := totalEvents - 1
	if expectedReplays < 1 {
		t.Skip("need at least 2 stored events to verify replay")
	}

	// Read replayed events. They arrive before the endpoint event stream resumes.
	for i := 0; i < expectedReplays; i++ {
		ev, err := readSSEEvent(reader2)
		if err != nil {
			t.Fatalf("expected replayed event %d/%d, got error: %v", i+1, expectedReplays, err)
		}
		if ev.Event != "message" {
			t.Errorf("replayed event %d type = %q, want 'message'", i+1, ev.Event)
		}
	}
}

// TestSSENoGracePeriodDefault verifies backward compatibility: without
// WithSSEGracePeriod, disconnecting an SSE connection immediately destroys
// the session. A POST to the old session ID returns 410 Gone right away.
func TestSSENoGracePeriodDefault(t *testing.T) {
	ts, _ := testMCPServer(
		WithSSE(true),
		WithStreamableHTTP(false),
		// No WithSSEGracePeriod — default is 0 (immediate cleanup).
	)
	defer ts.Close()

	sseResp, reader, postURL, err := connectSSE(ts, "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	initializeSSESession(t, postURL, reader)

	// Disconnect.
	sseResp.Body.Close()
	time.Sleep(200 * time.Millisecond)

	// POST should immediately return 410 (no grace period).
	resp, _ := postJSON(postURL, &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`5`), Method: "tools/list",
	})
	if resp.StatusCode != http.StatusGone {
		t.Errorf("without grace period: POST status = %d, want %d (Gone)", resp.StatusCode, http.StatusGone)
	}
	resp.Body.Close()
}

// TestSSEReconnectPreservesState verifies that after reconnecting within the
// grace period, the session retains its initialized state — tools registered
// before the disconnect are still visible via tools/list without re-initializing.
func TestSSEReconnectPreservesState(t *testing.T) {
	ts, _ := testMCPServer(
		WithSSE(true),
		WithStreamableHTTP(false),
		WithSSEGracePeriod(5*time.Second),
	)
	defer ts.Close()

	// Connect, initialize, verify echo tool available.
	sseResp, reader, postURL, err := connectSSE(ts, "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	sessionID := extractSessionID(postURL)
	initializeSSESession(t, postURL, reader)

	// Call tools/list to verify echo tool exists.
	postJSON(postURL, &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "tools/list",
	})
	toolsEv, _ := readSSEEvent(reader)
	if !strings.Contains(toolsEv.Data, `"echo"`) {
		t.Fatalf("tools/list should contain echo tool, got: %s", toolsEv.Data)
	}

	// Disconnect.
	sseResp.Body.Close()
	time.Sleep(200 * time.Millisecond)

	// Reconnect.
	sseResp2, reader2, postURL2, err := connectSSEWithParams(t, ts.URL, "/mcp",
		map[string]string{"sessionId": sessionID}, nil)
	if err != nil {
		t.Fatalf("reconnect failed: %v", err)
	}
	defer sseResp2.Body.Close()

	// tools/list should still return echo (same Dispatcher, same state).
	postJSON(postURL2, &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`3`), Method: "tools/list",
	})
	toolsEv2, _ := readSSEEvent(reader2)
	if !strings.Contains(toolsEv2.Data, `"echo"`) {
		t.Fatalf("tools/list after reconnect should contain echo tool, got: %s", toolsEv2.Data)
	}
}
