package e2e_test

// Transport-level auth tests. These verify that authentication is enforced
// on every HTTP endpoint for both Streamable HTTP and SSE transports, including
// the auth gap fixes from PR #51 (SSE GET and Streamable DELETE).
//
// These tests operate at the HTTP layer (not through the MCP client) to verify
// the transport-level auth checks directly.

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestE2E_Streamable_AuthRequired verifies that the Streamable HTTP POST /mcp
// endpoint rejects unauthenticated requests with 401. Per MCP spec S13: "auth
// MUST be included in every HTTP request (even same session)".
func TestE2E_Streamable_AuthRequired(t *testing.T) {
	env := NewTestEnv(t)

	resp := RawPOST(t, env.MCPServerURL+"/mcp", "", initializeJSON)
	defer resp.Body.Close()
	assert.Equal(t, 401, resp.StatusCode)
}

// TestE2E_Streamable_DeleteRequiresAuth verifies that DELETE /mcp rejects
// unauthenticated requests. This was an auth gap fixed in PR #51 — previously
// DELETE was unauthenticated, allowing anyone to terminate sessions.
func TestE2E_Streamable_DeleteRequiresAuth(t *testing.T) {
	env := NewTestEnv(t)

	resp := RawDELETE(t, env.MCPServerURL+"/mcp", "", "fake-session-id")
	defer resp.Body.Close()
	assert.Equal(t, 401, resp.StatusCode)
}

// TestE2E_Streamable_ValidAuth_Works verifies that an authenticated POST /mcp
// with a valid token succeeds (HTTP 200). This is the transport-level complement
// to TestE2E_ValidToken_ToolCall which tests at the MCP client level.
func TestE2E_Streamable_ValidAuth_Works(t *testing.T) {
	env := NewTestEnv(t)
	token := env.MintToken(t, "user-1", []string{"tools:read"})

	resp := RawPOST(t, env.MCPServerURL+"/mcp", token, initializeJSON)
	defer resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)
}

// TestE2E_SSE_AuthOnGET verifies that the SSE GET /mcp/sse endpoint rejects
// unauthenticated connections. This was an auth gap fixed in PR #51 — the SSE
// GET was previously unauthenticated, allowing anyone to open a session.
func TestE2E_SSE_AuthOnGET(t *testing.T) {
	env := NewTestEnv(t)

	resp := RawGET(t, env.MCPServerURL+"/mcp/sse", "")
	defer resp.Body.Close()
	// SSE endpoint should reject without auth (401 or 403)
	assert.True(t, resp.StatusCode == 401 || resp.StatusCode == 403,
		"expected 401 or 403, got %d", resp.StatusCode)
}

// TestE2E_SSE_AuthOnPOST verifies that POST /mcp/message rejects unauthenticated
// requests. Combined with TestE2E_SSE_AuthOnGET, this ensures auth is checked
// on both SSE endpoints.
func TestE2E_SSE_AuthOnPOST(t *testing.T) {
	env := NewTestEnv(t)

	resp := RawPOST(t, env.MCPServerURL+"/mcp/message?sessionId=fake", "",
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	defer resp.Body.Close()
	assert.Equal(t, 401, resp.StatusCode)
}
