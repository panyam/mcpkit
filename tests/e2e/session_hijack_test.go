package e2e_test

// Session hijacking protection e2e tests. Verifies that the Streamable HTTP
// transport binds the authenticated principal (Claims.Subject) to the session
// at creation time, and rejects requests from different principals.
//
// Uses real RS256 JWTs issued by oneauth's TestAuthServer and validated by
// mcpkit's JWTValidator via JWKS — the full auth stack.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initializeSession sends an initialize request with the given token and returns
// the Mcp-Session-Id from the response.
func initializeSession(t *testing.T, url, token string) string {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"hijack-test","version":"1.0"}}}`
	resp := RawPOST(t, url+"/mcp", token, body)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "initialize should succeed")
	sessionID := resp.Header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID, "initialize should return Mcp-Session-Id")
	return sessionID
}

// TestSessionHijack_DifferentUserRejected verifies end-to-end that a session
// created by user A cannot be used by user B, even when user B has a valid JWT.
// Both tokens are real RS256 JWTs validated via JWKS — this is the full auth stack.
func TestSessionHijack_DifferentUserRejected(t *testing.T) {
	env := NewTestEnv(t)

	// Mint tokens for two different users.
	tokenAlice := env.MintToken(t, "alice", testScopes)
	tokenBob := env.MintToken(t, "bob", testScopes)

	// Alice creates a session.
	sessionID := initializeSession(t, env.MCPServerURL, tokenAlice)

	// Alice can use the session.
	resp := rawPOSTWithSession(t, env.MCPServerURL+"/mcp", tokenAlice, sessionID,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode, "Alice should access her own session")

	// Bob tries to hijack Alice's session → 403.
	hijackResp := rawPOSTWithSession(t, env.MCPServerURL+"/mcp", tokenBob, sessionID,
		`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	defer hijackResp.Body.Close()
	assert.Equal(t, http.StatusForbidden, hijackResp.StatusCode,
		"Bob should be rejected from Alice's session")
}

// TestSessionHijack_DeleteRejected verifies that a different user cannot
// DELETE another user's session.
func TestSessionHijack_DeleteRejected(t *testing.T) {
	env := NewTestEnv(t)

	tokenAlice := env.MintToken(t, "alice", testScopes)
	tokenBob := env.MintToken(t, "bob", testScopes)

	sessionID := initializeSession(t, env.MCPServerURL, tokenAlice)

	// Bob tries to delete Alice's session → 403.
	resp := RawDELETE(t, env.MCPServerURL+"/mcp", tokenBob, sessionID)
	resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"Bob should not be able to delete Alice's session")

	// Alice can still use her session.
	aliceResp := rawPOSTWithSession(t, env.MCPServerURL+"/mcp", tokenAlice, sessionID,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	aliceResp.Body.Close()
	assert.Equal(t, http.StatusOK, aliceResp.StatusCode,
		"Alice's session should survive Bob's failed delete")
}

// rawPOSTWithSession sends a POST with both Authorization and Mcp-Session-Id headers.
func rawPOSTWithSession(t *testing.T, url, token, sessionID, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest("POST", url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("rawPOSTWithSession: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", StreamableHTTPAccept)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("rawPOSTWithSession: %v", err)
	}
	return resp
}
