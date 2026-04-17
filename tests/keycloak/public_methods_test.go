package keycloak_test

// Keycloak interop test for pre-auth capability discovery (#265).
// Verifies that WithPublicMethods allows unauthenticated tools/list
// against a Keycloak-protected MCP server.

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestKeycloak_PublicMethods_DiscoverWithoutAuth verifies that an
// unauthenticated client can call tools/list on a Keycloak-protected
// server when WithPublicMethods is configured.
func TestKeycloak_PublicMethods_DiscoverWithoutAuth(t *testing.T) {
	skipIfKeycloakNotRunning(t)
	env := NewMCPTestEnv(t,
		server.WithPublicMethods("initialize", "notifications/initialized", "tools/list"),
	)

	// Initialize without auth → should succeed (public method).
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"discovery-test","version":"1.0"}}}`
	req, _ := http.NewRequest("POST", env.MCPServer.URL+"/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", streamableAccept)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "public initialize should succeed")
	sessionID := resp.Header.Get("Mcp-Session-Id")
	resp.Body.Close()
	require.NotEmpty(t, sessionID)

	// Send initialized notification.
	notifyBody := `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	nReq, _ := http.NewRequest("POST", env.MCPServer.URL+"/mcp", strings.NewReader(notifyBody))
	nReq.Header.Set("Content-Type", "application/json")
	nReq.Header.Set("Accept", streamableAccept)
	nReq.Header.Set("Mcp-Session-Id", sessionID)
	nResp, _ := http.DefaultClient.Do(nReq)
	nResp.Body.Close()

	// tools/list without auth → should succeed (public method).
	listBody := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	lReq, _ := http.NewRequest("POST", env.MCPServer.URL+"/mcp", strings.NewReader(listBody))
	lReq.Header.Set("Content-Type", "application/json")
	lReq.Header.Set("Accept", streamableAccept)
	lReq.Header.Set("Mcp-Session-Id", sessionID)
	lResp, err := http.DefaultClient.Do(lReq)
	require.NoError(t, err)
	defer lResp.Body.Close()
	assert.Equal(t, http.StatusOK, lResp.StatusCode, "public tools/list should succeed without auth")
	data, _ := io.ReadAll(lResp.Body)
	assert.Contains(t, string(data), "echo", "tools/list should return registered tools")
}

// TestKeycloak_PublicMethods_ToolCallRequiresAuth verifies that non-public
// methods still require a valid Keycloak JWT.
func TestKeycloak_PublicMethods_ToolCallRequiresAuth(t *testing.T) {
	skipIfKeycloakNotRunning(t)
	env := NewMCPTestEnv(t,
		server.WithPublicMethods("initialize", "notifications/initialized", "tools/list"),
	)

	// tools/call without auth → should fail.
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"msg":"test"}}}`
	req, _ := http.NewRequest("POST", env.MCPServer.URL+"/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", streamableAccept)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"tools/call should require Keycloak auth")
}
