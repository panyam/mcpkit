package server_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClientSuggestsValidSessionID verifies that when a client includes
// _suggestedSessionId in the initialize request params, the server uses
// it as the actual session ID (returned in the Mcp-Session-Id header).
func TestClientSuggestsValidSessionID(t *testing.T) {
	srv := testutil.NewTestServer()
	handler := srv.Handler(server.WithStreamableHTTP(true), server.WithSSE(false))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1.0"},"_suggestedSessionId":"my-custom-session-42"}}`
	resp, err := http.Post(ts.URL+"/mcp", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	sessionID := resp.Header.Get("Mcp-Session-Id")
	assert.Equal(t, "my-custom-session-42", sessionID, "server should use the suggested session ID")
}

// TestClientSuggestsDuplicateSessionID verifies that when a client suggests
// a session ID that's already in use, the server rejects it and assigns
// a new random ID instead.
func TestClientSuggestsDuplicateSessionID(t *testing.T) {
	srv := testutil.NewTestServer()
	handler := srv.Handler(server.WithStreamableHTTP(true), server.WithSSE(false))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	// First session claims "shared-id"
	body1 := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1.0"},"_suggestedSessionId":"shared-id"}}`
	resp1, _ := http.Post(ts.URL+"/mcp", "application/json", strings.NewReader(body1))
	resp1.Body.Close()
	assert.Equal(t, "shared-id", resp1.Header.Get("Mcp-Session-Id"))

	// Second session tries to claim "shared-id" — should get a different ID
	body2 := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test2","version":"1.0"},"_suggestedSessionId":"shared-id"}}`
	resp2, _ := http.Post(ts.URL+"/mcp", "application/json", strings.NewReader(body2))
	resp2.Body.Close()
	sid2 := resp2.Header.Get("Mcp-Session-Id")
	assert.NotEqual(t, "shared-id", sid2, "duplicate suggestion should be rejected")
	assert.NotEmpty(t, sid2, "server should still assign a session ID")
}

// TestClientSuggestsInvalidSessionID verifies that invalid session IDs
// (too long, special characters) are rejected and the server assigns its own.
func TestClientSuggestsInvalidSessionID(t *testing.T) {
	srv := testutil.NewTestServer()
	handler := srv.Handler(server.WithStreamableHTTP(true), server.WithSSE(false))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	cases := []struct {
		name string
		id   string
	}{
		{"spaces", "has spaces"},
		{"special chars", "id@#$%"},
		{"too long", strings.Repeat("a", 200)},
		{"empty", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1.0"},"_suggestedSessionId":"` + tc.id + `"}}`
			resp, err := http.Post(ts.URL+"/mcp", "application/json", strings.NewReader(body))
			require.NoError(t, err)
			resp.Body.Close()

			sid := resp.Header.Get("Mcp-Session-Id")
			assert.NotEmpty(t, sid, "should still get a session ID")
			if tc.id != "" {
				assert.NotEqual(t, tc.id, sid, "invalid ID should be rejected")
			}
		})
	}
}

// TestNoSuggestionServerAssigns verifies that when no _suggestedSessionId
// is provided, the server assigns a random session ID (existing behavior).
func TestNoSuggestionServerAssigns(t *testing.T) {
	srv := testutil.NewTestServer()
	handler := srv.Handler(server.WithStreamableHTTP(true), server.WithSSE(false))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`
	resp, err := http.Post(ts.URL+"/mcp", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	resp.Body.Close()

	sid := resp.Header.Get("Mcp-Session-Id")
	assert.NotEmpty(t, sid, "server should assign a session ID")
	assert.Greater(t, len(sid), 10, "server-generated ID should be reasonably long")
}
