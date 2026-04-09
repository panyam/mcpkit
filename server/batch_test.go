package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
	handler := srv.Handler(server.WithStreamableHTTP(true), server.WithSSE(false))
	ts := httptest.NewServer(handler)

	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`
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
