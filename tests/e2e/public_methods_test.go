package e2e_test

// E2E test for pre-auth capability discovery (#76). Verifies that
// WithPublicMethods allows unauthenticated tools/list while tools/call
// still requires a valid JWT. Uses real RS256 JWTs via oneauth.

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPublicMethods_E2E_DiscoveryThenAuth verifies the full pre-auth discovery
// flow with real JWT auth: unauthenticated tools/list succeeds (public method),
// then authenticated tools/call works after obtaining a token.
func TestPublicMethods_E2E_DiscoveryThenAuth(t *testing.T) {
	env := NewTestEnvWithPublicMethods(t,
		"initialize", "notifications/initialized", "tools/list",
	)

	// Step 1: Unauthenticated tools/list → should succeed (public method).
	// First need to initialize (also public).
	initResp := rawPOSTNoSession(t, env.MCPServerURL+"/mcp", "",
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"discovery-test","version":"1.0"}}}`)
	require.Equal(t, http.StatusOK, initResp.StatusCode, "public initialize should succeed without auth")
	sessionID := initResp.Header.Get("Mcp-Session-Id")
	initResp.Body.Close()
	require.NotEmpty(t, sessionID)

	// Send initialized notification.
	notifyResp := rawPOSTWithSession(t, env.MCPServerURL+"/mcp", "", sessionID,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	notifyResp.Body.Close()

	// tools/list without auth → should succeed.
	listResp := rawPOSTWithSession(t, env.MCPServerURL+"/mcp", "", sessionID,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	defer listResp.Body.Close()
	require.Equal(t, http.StatusOK, listResp.StatusCode, "public tools/list should succeed without auth")
	body, _ := io.ReadAll(listResp.Body)
	assert.Contains(t, string(body), "echo", "tools/list should return registered tools")

	// Step 2: tools/call without auth → should fail (not public).
	callResp := rawPOSTWithSession(t, env.MCPServerURL+"/mcp", "", sessionID,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"msg":"test"}}}`)
	callResp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, callResp.StatusCode,
		"tools/call should require auth")

	// Step 3: Get a token and call tools/call with auth → should succeed.
	token := env.MintToken(t, "alice", testScopes)
	authCallResp := rawPOSTWithSession(t, env.MCPServerURL+"/mcp", token, sessionID,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"echo","arguments":{"msg":"hello"}}}`)
	defer authCallResp.Body.Close()
	assert.Equal(t, http.StatusOK, authCallResp.StatusCode,
		"authenticated tools/call should succeed")
}

// rawPOSTNoSession sends a POST without Mcp-Session-Id header.
func rawPOSTNoSession(t *testing.T, url, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest("POST", url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("rawPOSTNoSession: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", StreamableHTTPAccept)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("rawPOSTNoSession: %v", err)
	}
	return resp
}
