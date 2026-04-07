package keycloak_test

// Keycloak interop tests for core. These prove that mcpkit's JWTValidator
// correctly validates tokens issued by a real Keycloak instance, not just
// in-process oneauth tokens. This is the highest-confidence auth validation:
// real IdP → real JWKS → real JWT verification → real MCP tool call.
//
// Prerequisites:
//   - Keycloak running at localhost:8180 (or KEYCLOAK_URL env var)
//   - Realm "mcpkit-test" imported from realm.json
//   - Run: make upkcl  (starts Keycloak container with realm auto-import)
//   - Run: make test-auth-keycloak (runs these tests)
//
// Tests skip gracefully when Keycloak is not reachable.

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/oneauth/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestKeycloak_MCPServer_ValidToken verifies that a Keycloak-issued JWT
// (obtained via client_credentials) is accepted by mcpkit's JWTValidator
// and allows a tool call to succeed. This is the primary interop test.
func TestKeycloak_MCPServer_ValidToken(t *testing.T) {
	skipIfKeycloakNotRunning(t)
	env := NewMCPTestEnv(t)

	tok := getClientCredentialsToken(t, env.OIDC.TokenEndpoint, scopeToolsRead, scopeToolsCall)

	client := core.NewClient(
		env.MCPServer.URL+"/mcp",
		core.ClientInfo{Name: "keycloak-test", Version: "0.1.0"},
		core.WithClientBearerToken(tok.AccessToken),
	)
	require.NoError(t, client.Connect())
	defer client.Close()

	result, err := client.ToolCall("echo", map[string]any{"msg": "from-keycloak"})
	require.NoError(t, err)
	assert.Contains(t, result, "from-keycloak")
}

// TestKeycloak_MCPServer_TamperedToken verifies that modifying a Keycloak
// token's payload causes mcpkit's JWTValidator to reject it. This tests the
// RS256 signature verification against Keycloak's JWKS-published key.
func TestKeycloak_MCPServer_TamperedToken(t *testing.T) {
	skipIfKeycloakNotRunning(t)
	env := NewMCPTestEnv(t)

	tok := getClientCredentialsToken(t, env.OIDC.TokenEndpoint)

	// Tamper with the token
	tampered := tok.AccessToken[:len(tok.AccessToken)-5] + "XXXXX"

	req, _ := http.NewRequest("POST", env.MCPServer.URL+"/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"0.1"}}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+tampered)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, 401, resp.StatusCode)
}

// TestKeycloak_MCPServer_ScopeAllowed verifies that a Keycloak token with
// the tools-call scope can invoke a tool that requires it via RequireScope.
func TestKeycloak_MCPServer_ScopeAllowed(t *testing.T) {
	skipIfKeycloakNotRunning(t)
	env := NewMCPTestEnv(t)

	tok := getClientCredentialsToken(t, env.OIDC.TokenEndpoint, scopeToolsRead, scopeToolsCall)

	client := core.NewClient(
		env.MCPServer.URL+"/mcp",
		core.ClientInfo{Name: "keycloak-test", Version: "0.1.0"},
		core.WithClientBearerToken(tok.AccessToken),
	)
	require.NoError(t, client.Connect())
	defer client.Close()

	result, err := client.ToolCall("scoped-tool", nil)
	require.NoError(t, err)
	assert.Equal(t, "ok", result)
}

// TestKeycloak_MCPServer_ScopeDenied verifies that a Keycloak token WITHOUT
// the required scope is denied by RequireScope. The tool returns an error
// message (scope enforcement at the tool level, not transport level).
func TestKeycloak_MCPServer_ScopeDenied(t *testing.T) {
	skipIfKeycloakNotRunning(t)
	env := NewMCPTestEnv(t)

	// Token with only tools-read, NOT tools-call
	tok := getClientCredentialsToken(t, env.OIDC.TokenEndpoint, scopeToolsRead)

	client := core.NewClient(
		env.MCPServer.URL+"/mcp",
		core.ClientInfo{Name: "keycloak-test", Version: "0.1.0"},
		core.WithClientBearerToken(tok.AccessToken),
	)
	require.NoError(t, client.Connect())
	defer client.Close()

	result, err := client.ToolCall("scoped-tool", nil)
	require.NoError(t, err)
	assert.Contains(t, result, "error")
}

// TestKeycloak_MCPServer_PRM verifies that the MCP server's PRM endpoint
// includes Keycloak's issuer URL as an authorization_server.
func TestKeycloak_MCPServer_PRM(t *testing.T) {
	skipIfKeycloakNotRunning(t)
	env := NewMCPTestEnv(t)

	resp, err := http.Get(env.MCPServer.URL + "/.well-known/oauth-protected-resource/mcp")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)

	var prm map[string]any
	body, _ := io.ReadAll(resp.Body)
	require.NoError(t, json.Unmarshal(body, &prm))

	servers, ok := prm["authorization_servers"].([]any)
	require.True(t, ok)
	assert.Contains(t, servers, env.OIDC.Issuer)
}

// TestKeycloak_MCPServer_WWWAuthenticate verifies that an unauthenticated
// request to the MCP server returns 401 with a parseable WWW-Authenticate
// header, even when the server is configured with Keycloak as the AS.
func TestKeycloak_MCPServer_WWWAuthenticate(t *testing.T) {
	skipIfKeycloakNotRunning(t)
	env := NewMCPTestEnv(t)

	req, _ := http.NewRequest("POST", env.MCPServer.URL+"/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, 401, resp.StatusCode)
	wwwAuth := resp.Header.Get("WWW-Authenticate")
	assert.NotEmpty(t, wwwAuth)
	assert.Contains(t, wwwAuth, "Bearer")
	assert.Contains(t, wwwAuth, "resource_metadata=")
}

// TestKeycloak_MCPServer_PasswordGrant verifies that a Keycloak token obtained
// via the password grant for the test user is accepted by the MCP server.
// The claims should contain the user's subject (Keycloak user ID).
func TestKeycloak_MCPServer_PasswordGrant(t *testing.T) {
	skipIfKeycloakNotRunning(t)
	env := NewMCPTestEnv(t)

	tok := getPasswordToken(t, env.OIDC.TokenEndpoint)

	// Verify the token has user claims
	claims := testutil.ParseJWTClaims(t, tok.AccessToken)
	assert.NotEmpty(t, claims["sub"], "password grant token should have sub claim")

	client := core.NewClient(
		env.MCPServer.URL+"/mcp",
		core.ClientInfo{Name: "keycloak-test", Version: "0.1.0"},
		core.WithClientBearerToken(tok.AccessToken),
	)
	require.NoError(t, client.Connect())
	defer client.Close()

	result, err := client.ToolCall("echo", map[string]any{"msg": "from-user"})
	require.NoError(t, err)
	assert.Contains(t, result, "from-user")

	// Verify the echo tool reports the user's subject
	var data map[string]any
	require.NoError(t, json.Unmarshal([]byte(result), &data))
	assert.NotEmpty(t, data["sub"], "echo should report user's subject from claims")
}
