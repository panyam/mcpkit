package keycloak_test

// Keycloak interop test for session hijacking protection (#258).
// Two real Keycloak users with different sub claims — verifies that
// one user cannot use another user's MCP session.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/panyam/oneauth/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const streamableAccept = "application/json, text/event-stream"

// kcInitSession initializes an MCP session with a Keycloak-issued token.
func kcInitSession(t *testing.T, url, token string) string {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"kcl-hijack-test","version":"1.0"}}}`
	req, _ := http.NewRequest("POST", url+"/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", streamableAccept)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "initialize should succeed")
	sessionID := resp.Header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID)
	return sessionID
}

// kcPostToSession sends a tools/list request to an existing session.
func kcPostToSession(t *testing.T, url, token, sessionID string) *http.Response {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	req, _ := http.NewRequest("POST", url+"/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", streamableAccept)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Mcp-Session-Id", sessionID)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// TestKeycloak_SessionHijack_DifferentUserRejected verifies that user B
// (mcp-testuser2) cannot use user A's (mcp-testuser) MCP session.
// Both tokens are real Keycloak JWTs with different sub claims.
func TestKeycloak_SessionHijack_DifferentUserRejected(t *testing.T) {
	skipIfKeycloakNotRunning(t)
	env := NewMCPTestEnv(t)

	// Get tokens for two different users via password grant.
	tokA := getPasswordTokenForUser(t, env.OIDC.TokenEndpoint, testUsername, testPassword)
	tokB := getPasswordTokenForUser(t, env.OIDC.TokenEndpoint, testUsername2, testPassword2)

	// Verify they have different sub claims.
	claimsA := testutil.ParseJWTClaims(t, tokA.AccessToken)
	claimsB := testutil.ParseJWTClaims(t, tokB.AccessToken)
	require.NotEqual(t, claimsA["sub"], claimsB["sub"],
		"two different users must have different sub claims")

	// User A creates a session.
	sessionID := kcInitSession(t, env.MCPServer.URL, tokA.AccessToken)

	// User A can use the session.
	resp := kcPostToSession(t, env.MCPServer.URL, tokA.AccessToken, sessionID)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode, "user A should access their own session")

	// User B tries to use A's session → 403.
	resp = kcPostToSession(t, env.MCPServer.URL, tokB.AccessToken, sessionID)
	resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"user B should be rejected from user A's session")
}

// TestKeycloak_SessionHijack_SameUserAllowed verifies that the same Keycloak
// user can make multiple requests on their own session.
func TestKeycloak_SessionHijack_SameUserAllowed(t *testing.T) {
	skipIfKeycloakNotRunning(t)
	env := NewMCPTestEnv(t)

	tok := getPasswordTokenForUser(t, env.OIDC.TokenEndpoint, testUsername, testPassword)
	sessionID := kcInitSession(t, env.MCPServer.URL, tok.AccessToken)

	for i := 0; i < 3; i++ {
		resp := kcPostToSession(t, env.MCPServer.URL, tok.AccessToken, sessionID)
		resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode, "same user request %d should succeed", i)
	}
}
