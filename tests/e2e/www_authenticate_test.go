package e2e_test

// WWW-Authenticate header tests. These verify that unauthenticated requests
// to the MCP server receive proper RFC 6750 / MCP auth spec WWW-Authenticate
// headers containing the resource_metadata URL and supported scopes.

import (
	"testing"

	"github.com/panyam/mcpkit/ext/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2E_401_WWWAuthenticate_Present verifies that an unauthenticated
// request to the MCP server returns 401 with a WWW-Authenticate header.
// Per MCP spec S7: "Invalid/expired tokens → HTTP 401".
func TestE2E_401_WWWAuthenticate_Present(t *testing.T) {
	env := NewTestEnv(t)

	resp := RawPOST(t, env.MCPServerURL+"/mcp", "", initializeJSON)
	defer resp.Body.Close()

	assert.Equal(t, 401, resp.StatusCode)
	wwwAuth := resp.Header.Get("WWW-Authenticate")
	assert.NotEmpty(t, wwwAuth, "401 response should include WWW-Authenticate header")
	assert.Contains(t, wwwAuth, "Bearer", "WWW-Authenticate should use Bearer scheme")
}

// TestE2E_401_WWWAuthenticate_HasResourceMetadata verifies that the
// WWW-Authenticate header contains the resource_metadata parameter pointing
// to the PRM endpoint. Per MCP spec: the header guides clients to discover
// the authorization server via the PRM document.
func TestE2E_401_WWWAuthenticate_HasResourceMetadata(t *testing.T) {
	env := NewTestEnv(t)

	resp := RawPOST(t, env.MCPServerURL+"/mcp", "", initializeJSON)
	defer resp.Body.Close()

	wwwAuth := resp.Header.Get("WWW-Authenticate")
	require.NotEmpty(t, wwwAuth)
	assert.Contains(t, wwwAuth, "resource_metadata=")
	assert.Contains(t, wwwAuth, env.MCPServerURL)
}

// TestE2E_401_WWWAuthenticate_Parseable verifies that the WWW-Authenticate
// header can be round-tripped through auth.ParseWWWAuthenticate, returning
// the correct resource_metadata URL and scopes. This tests the integration
// between the server-side header builder and the client-side parser.
func TestE2E_401_WWWAuthenticate_Parseable(t *testing.T) {
	env := NewTestEnv(t)

	resp := RawPOST(t, env.MCPServerURL+"/mcp", "", initializeJSON)
	defer resp.Body.Close()

	wwwAuth := resp.Header.Get("WWW-Authenticate")
	require.NotEmpty(t, wwwAuth)

	rm, scopes, err := auth.ParseWWWAuthenticate(wwwAuth)
	require.NoError(t, err)

	// resource_metadata should point to the PRM endpoint
	assert.Contains(t, rm, env.MCPServerURL)
	assert.Contains(t, rm, ".well-known/oauth-protected-resource")

	// Scopes should match the configured AllScopes
	assert.Equal(t, len(testScopes), len(scopes), "scope count mismatch")
	for _, s := range testScopes {
		assert.Contains(t, scopes, s)
	}
}
