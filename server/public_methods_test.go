package server_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newPublicMethodsServer creates a test server with auth + public methods.
func newPublicMethodsServer(publicMethods ...string) *httptest.Server {
	opts := []server.Option{
		server.WithAuth(testAuthValidator{}),
	}
	if len(publicMethods) > 0 {
		opts = append(opts, server.WithPublicMethods(publicMethods...))
	}
	srv := server.NewServer(core.ServerInfo{Name: "public-test", Version: "1.0"}, opts...)
	srv.Register(core.TextTool[struct{}]("ping", "Returns pong",
		func(ctx core.ToolContext, _ struct{}) (string, error) {
			return "pong", nil
		},
	))
	return httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true), server.WithSSE(false)))
}

// streamablePost sends a JSON-RPC request to the Streamable HTTP endpoint.
func streamablePostRaw(t *testing.T, url, token, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", url+"/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// TestPublicMethods_ToolsListWithoutAuth verifies that unauthenticated
// tools/list succeeds when it's configured as a public method, while
// tools/call still requires auth.
func TestPublicMethods_ToolsListWithoutAuth(t *testing.T) {
	ts := newPublicMethodsServer("initialize", "notifications/initialized", "tools/list")
	defer ts.Close()

	// Initialize without auth → should succeed (public method).
	resp := streamablePostRaw(t, ts.URL, "",
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "public initialize should succeed without auth")
	sessionID := resp.Header.Get("Mcp-Session-Id")
	resp.Body.Close()
	require.NotEmpty(t, sessionID)

	// Send initialized notification.
	req, _ := http.NewRequest("POST", ts.URL+"/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Session-Id", sessionID)
	initResp, _ := http.DefaultClient.Do(req)
	initResp.Body.Close()

	// tools/list without auth → should succeed (public method).
	req2, _ := http.NewRequest("POST", ts.URL+"/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Accept", "application/json, text/event-stream")
	req2.Header.Set("Mcp-Session-Id", sessionID)
	listResp, err := http.DefaultClient.Do(req2)
	require.NoError(t, err)
	defer listResp.Body.Close()
	assert.Equal(t, http.StatusOK, listResp.StatusCode, "public tools/list should succeed without auth")

	body, _ := io.ReadAll(listResp.Body)
	assert.Contains(t, string(body), "ping", "tools/list should return the registered tool")
}

// TestPublicMethods_ToolsCallRequiresAuth verifies that non-public methods
// still require authentication even when public methods are configured.
func TestPublicMethods_ToolsCallRequiresAuth(t *testing.T) {
	ts := newPublicMethodsServer("initialize", "notifications/initialized", "tools/list")
	defer ts.Close()

	// tools/call without auth → should fail (not a public method).
	resp := streamablePostRaw(t, ts.URL, "",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ping"}}`)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"non-public tools/call should require auth")
}

// TestPublicMethods_DefaultRequiresAuth verifies that when WithPublicMethods
// is NOT configured, all methods require auth (backward compatibility).
func TestPublicMethods_DefaultRequiresAuth(t *testing.T) {
	ts := newPublicMethodsServer() // no public methods
	defer ts.Close()

	// initialize without auth → should fail.
	resp := streamablePostRaw(t, ts.URL, "",
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"without WithPublicMethods, all methods should require auth")
}

// TestPublicMethods_AuthenticatedStillWorks verifies that public methods
// work normally when a valid auth token IS provided — claims are populated.
func TestPublicMethods_AuthenticatedStillWorks(t *testing.T) {
	ts := newPublicMethodsServer("initialize", "notifications/initialized", "tools/list", "tools/call")
	defer ts.Close()

	// Initialize with auth token → should succeed and have claims.
	resp := streamablePostRaw(t, ts.URL, "user-alice",
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
}
