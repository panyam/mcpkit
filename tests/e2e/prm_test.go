package e2e_test

// Protected Resource Metadata (PRM) endpoint tests. These verify that
// auth.MountAuth correctly serves RFC 9728 metadata at the well-known URIs,
// with the correct resource, authorization_servers, and scopes_supported fields.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2E_PRM_PathBased verifies that the path-based PRM endpoint at
// /.well-known/oauth-protected-resource/mcp returns valid JSON with the
// expected fields. Per RFC 9728, path-based discovery uses the resource
// server's path appended to the well-known prefix.
func TestE2E_PRM_PathBased(t *testing.T) {
	env := NewTestEnv(t)

	resp := RawGET(t, env.MCPServerURL+"/.well-known/oauth-protected-resource/mcp", "")
	body := ReadBody(t, resp)
	assert.Equal(t, 200, resp.StatusCode)

	var prm map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &prm))
	assert.Equal(t, env.MCPServerURL, prm["resource"])
}

// TestE2E_PRM_RootFallback verifies that the root PRM endpoint at
// /.well-known/oauth-protected-resource also returns valid metadata.
// Per MCP spec, clients try path-based first, then fall back to root.
func TestE2E_PRM_RootFallback(t *testing.T) {
	env := NewTestEnv(t)

	resp := RawGET(t, env.MCPServerURL+"/.well-known/oauth-protected-resource", "")
	body := ReadBody(t, resp)
	assert.Equal(t, 200, resp.StatusCode)

	var prm map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &prm))
	assert.Equal(t, env.MCPServerURL, prm["resource"])
}

// TestE2E_PRM_Content verifies the full PRM document content: resource URI
// matches the MCP server URL, authorization_servers includes the auth server,
// and scopes_supported matches the configured test scopes.
func TestE2E_PRM_Content(t *testing.T) {
	env := NewTestEnv(t)

	resp := RawGET(t, env.MCPServerURL+"/.well-known/oauth-protected-resource/mcp", "")
	body := ReadBody(t, resp)
	require.Equal(t, 200, resp.StatusCode)

	var prm map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &prm))

	// Resource URI
	assert.Equal(t, env.MCPServerURL, prm["resource"])

	// Authorization servers
	servers, ok := prm["authorization_servers"].([]any)
	require.True(t, ok, "authorization_servers should be an array")
	assert.Contains(t, servers, env.AS.URL())

	// Scopes supported
	scopes, ok := prm["scopes_supported"].([]any)
	require.True(t, ok, "scopes_supported should be an array")
	for _, s := range testScopes {
		assert.Contains(t, scopes, s)
	}
}
