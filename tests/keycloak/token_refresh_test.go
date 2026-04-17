package keycloak_test

// Keycloak interop test for token refresh (#106).
// Verifies that refresh_token grant works with Keycloak and produces
// a valid token that the MCP server accepts.

import (
	"net/url"
	"testing"

	client "github.com/panyam/mcpkit/client"
	core "github.com/panyam/mcpkit/core"
	"github.com/panyam/oneauth/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestKeycloak_TokenRefresh_PasswordGrant verifies that a refresh_token from
// a Keycloak password grant can be exchanged for a new access_token, and the
// refreshed token is accepted by the MCP server.
func TestKeycloak_TokenRefresh_PasswordGrant(t *testing.T) {
	skipIfKeycloakNotRunning(t)
	env := NewMCPTestEnv(t)

	// Step 1: Get initial token with password grant (includes refresh_token).
	tok := getPasswordTokenForUser(t, env.OIDC.TokenEndpoint, testUsername, testPassword)
	require.NotEmpty(t, tok.RefreshToken, "password grant should include refresh_token")

	// Step 2: Verify initial token works with MCP server.
	c := client.NewClient(
		env.MCPServer.URL+"/mcp",
		core.ClientInfo{Name: "refresh-test", Version: "1.0"},
		client.WithClientBearerToken(tok.AccessToken),
	)
	require.NoError(t, c.Connect())
	result, err := c.ToolCall("echo", map[string]any{"msg": "initial"})
	require.NoError(t, err)
	assert.Contains(t, result, "initial")
	c.Close()

	// Step 3: Exchange refresh_token for a new access_token.
	refreshed := refreshToken(t, env.OIDC.TokenEndpoint, tok.RefreshToken)
	require.NotEmpty(t, refreshed.AccessToken, "refresh should return new access_token")

	// Step 4: Verify refreshed token has same subject but different token value.
	origClaims := testutil.ParseJWTClaims(t, tok.AccessToken)
	newClaims := testutil.ParseJWTClaims(t, refreshed.AccessToken)
	assert.Equal(t, origClaims["sub"], newClaims["sub"],
		"refreshed token should have same subject")
	assert.NotEqual(t, tok.AccessToken, refreshed.AccessToken,
		"refreshed token should be a different string")

	// Step 5: Verify refreshed token works with MCP server.
	c2 := client.NewClient(
		env.MCPServer.URL+"/mcp",
		core.ClientInfo{Name: "refresh-test", Version: "1.0"},
		client.WithClientBearerToken(refreshed.AccessToken),
	)
	require.NoError(t, c2.Connect())
	defer c2.Close()
	result2, err := c2.ToolCall("echo", map[string]any{"msg": "refreshed"})
	require.NoError(t, err)
	assert.Contains(t, result2, "refreshed")
}

// refreshToken exchanges a refresh_token for a new access_token via Keycloak's
// token endpoint using the refresh_token grant type.
func refreshToken(t *testing.T, tokenEndpoint, refreshTok string) testutil.TokenResponse {
	t.Helper()
	return testutil.PostTokenEndpoint(t, tokenEndpoint, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshTok},
		"client_id":     {confidentialClientID},
		"client_secret": {confidentialClientSecret},
	})
}
