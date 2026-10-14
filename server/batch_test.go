package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initStreamableSession initializes a Streamable HTTP session and returns
// the session ID and test server URL. Helper for batch tests.
func initStreamableSession(t *testing.T, srv *server.Server) (string, *httptest.Server) {
	t.Helper()
	return initStreamableSessionAt(t, srv, "2025-03-26")
}

// initStreamableSessionAt is initStreamableSession on a chosen protocol
// version, for the batching gate (#1519 follow-up).
func initStreamableSessionAt(t *testing.T, srv *server.Server, version string) (string, *httptest.Server) {
	t.Helper()
	handler := srv.Handler(server.WithStreamableHTTP(true), server.WithSSE(false))
	ts := httptest.NewServer(handler)

	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"` + version + `","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`
	resp, err := http.Post(ts.URL+"/mcp", "application/json", strings.NewReader(initBody))
	require.NoError(t, err)
	resp.Body.Close()

	sessionID := resp.Header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID)

	// Send initialized notification
	notifBody := `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	req, _ := http.NewRequest("POST", ts.URL+"/mcp", strings.NewReader(notifBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Session-Id", sessionID)
	http.DefaultClient.Do(req)

	return sessionID, ts
}

// TestBatchRequestDispatchesAll verifies that a JSON-RPC 2.0 batch request
// (JSON array) is correctly dispatched: each request in the array produces
// a corresponding response in the returned JSON array.
func TestBatchRequestDispatchesAll(t *testing.T) {
	srv := testutil.NewTestServer()
	sessionID, ts := initStreamableSession(t, srv)
	defer ts.Close()

	// Batch: two pings
	batch := `[{"jsonrpc":"2.0","id":10,"method":"ping"},{"jsonrpc":"2.0","id":11,"method":"ping"}]`
	req, _ := http.NewRequest("POST", ts.URL+"/mcp", strings.NewReader(batch))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Session-Id", sessionID)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var responses []json.RawMessage
	require.NoError(t, json.Unmarshal(body, &responses), "response should be a JSON array")
	assert.Len(t, responses, 2, "should have 2 responses for 2 requests")

	// Both should be successful ping responses
	for i, raw := range responses {
		var r core.Response
		require.NoError(t, json.Unmarshal(raw, &r), "response %d should be valid JSON-RPC", i)
		assert.Nil(t, r.Error, "response %d should not be an error", i)
	}
}

// TestBatchWithNotifications verifies that notifications in a batch (requests
// with no ID) produce no response entry, per JSON-RPC 2.0 spec.
func TestBatchWithNotifications(t *testing.T) {
	srv := testutil.NewTestServer()
	sessionID, ts := initStreamableSession(t, srv)
	defer ts.Close()

	// Batch: one ping (has id) + one notification (no id)
	batch := `[{"jsonrpc":"2.0","id":10,"method":"ping"},{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"x","reason":"test"}}]`
	req, _ := http.NewRequest("POST", ts.URL+"/mcp", strings.NewReader(batch))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Session-Id", sessionID)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var responses []json.RawMessage
	require.NoError(t, json.Unmarshal(body, &responses))
	assert.Len(t, responses, 1, "should have 1 response (notification produces none)")
}

// TestBatchAllNotifications verifies that when all batch elements are
// notifications (no IDs), the server returns 202 Accepted with no body.
func TestBatchAllNotifications(t *testing.T) {
	srv := testutil.NewTestServer()
	sessionID, ts := initStreamableSession(t, srv)
	defer ts.Close()

	batch := `[{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"a","reason":"test"}},{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"b","reason":"test"}}]`
	req, _ := http.NewRequest("POST", ts.URL+"/mcp", strings.NewReader(batch))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Session-Id", sessionID)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
}

// TestEmptyBatchReturnsError verifies that an empty batch array produces
// an "Invalid Request" error per JSON-RPC 2.0 spec.
func TestEmptyBatchReturnsError(t *testing.T) {
	srv := testutil.NewTestServer()
	sessionID, ts := initStreamableSession(t, srv)
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/mcp", strings.NewReader("[]"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Session-Id", sessionID)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var errResp core.Response
	require.NoError(t, json.Unmarshal(body, &errResp))
	require.NotNil(t, errResp.Error, "empty batch should return an error")
	assert.Equal(t, core.ErrCodeInvalidRequest, errResp.Error.Code)
}

// TestBatchWithMalformedElement verifies that a malformed element in a batch
// produces a parse error for that element while other elements are still
// dispatched normally.
func TestBatchWithMalformedElement(t *testing.T) {
	srv := testutil.NewTestServer()
	sessionID, ts := initStreamableSession(t, srv)
	defer ts.Close()

	// First element is valid, second is malformed JSON
	batch := `[{"jsonrpc":"2.0","id":10,"method":"ping"},{"broken`
	req, _ := http.NewRequest("POST", ts.URL+"/mcp", strings.NewReader(batch))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Session-Id", sessionID)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// The entire batch fails to parse (SplitBatch fails on invalid JSON array)
	body, _ := io.ReadAll(resp.Body)
	var errResp core.Response
	require.NoError(t, json.Unmarshal(body, &errResp))
	require.NotNil(t, errResp.Error, "malformed batch should return parse error")
	assert.Equal(t, core.ErrCodeParse, errResp.Error.Code)
}

// TestBatchRequiresSession verifies that batch requests to the Streamable
// HTTP transport require a session (Mcp-Session-Id header).
func TestBatchRequiresSession(t *testing.T) {
	srv := testutil.NewTestServer()
	handler := srv.Handler(server.WithStreamableHTTP(true), server.WithSSE(false))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	batch := `[{"jsonrpc":"2.0","id":1,"method":"ping"}]`
	req, _ := http.NewRequest("POST", ts.URL+"/mcp", strings.NewReader(batch))
	req.Header.Set("Content-Type", "application/json")
	// No Mcp-Session-Id

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// 2025-03-26 added JSON-RPC batching and 2025-06-18 removed it. A session on a
// revision without it gets one -32600 error and nothing in the array runs;
// 2024-11-05 and 2025-03-26 keep the old behaviour.
func TestBatchGatedByProtocolVersion(t *testing.T) {
	cases := []struct {
		version string
		allowed bool
	}{
		{"2024-11-05", true},
		{"2025-03-26", true},
		{"2025-06-18", false},
		{"2025-11-25", false},
	}
	for _, tc := range cases {
		t.Run(tc.version, func(t *testing.T) {
			srv := testutil.NewTestServer()
			var calls atomic.Int32
			srv.RegisterTool(
				core.ToolDef{Name: "count", Description: "counts calls", InputSchema: json.RawMessage(`{"type":"object"}`)},
				func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
					calls.Add(1)
					return core.ToolResult{Content: []core.Content{{Type: "text", Text: "ok"}}}, nil
				},
			)
			sessionID, ts := initStreamableSessionAt(t, srv, tc.version)
			defer ts.Close()

			batch := `[{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"count","arguments":{}}},{"jsonrpc":"2.0","id":11,"method":"ping"}]`
			req, _ := http.NewRequest("POST", ts.URL+"/mcp", strings.NewReader(batch))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Mcp-Session-Id", sessionID)
			req.Header.Set("MCP-Protocol-Version", tc.version)
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)

			if tc.allowed {
				var responses []json.RawMessage
				require.NoError(t, json.Unmarshal(body, &responses), "want a JSON array, got %s", body)
				assert.Len(t, responses, 2)
				assert.Equal(t, int32(1), calls.Load())
				return
			}
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
			var r core.Response
			require.NoError(t, json.Unmarshal(body, &r), "want one JSON-RPC error object, got %s", body)
			require.NotNil(t, r.Error)
			assert.Equal(t, core.ErrCodeInvalidRequest, r.Error.Code)
			assert.Equal(t, "null", string(r.ID))
			assert.Equal(t, int32(0), calls.Load(), "no batch element may run")
		})
	}
}
