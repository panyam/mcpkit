package client_test

// SSE endpoint URL resolution tests (issue #131).
//
// The MCP SSE transport sends an "endpoint" event containing the URL where the
// client should POST JSON-RPC requests. Per RFC 3986, this may be an absolute
// URL, an absolute path, or a relative path. The client must resolve it against
// the SSE connection URL.
//
// These tests verify ResolveEndpointURL (unit tests) and the full SSE connect
// flow (integration test) to ensure correct URL resolution and sessionId
// extraction.

import (
	"testing"

	client "github.com/panyam/mcpkit/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveEndpointURL_AbsoluteURL verifies that an absolute URL in the
// endpoint event is returned unchanged. This is the default case — the server
// sends a fully qualified URL like "http://host/mcp/message?sessionId=abc".
func TestResolveEndpointURL_AbsoluteURL(t *testing.T) {
	resolved, err := client.ResolveEndpointURL(
		"http://localhost:8080/mcp/sse",
		"http://localhost:8080/mcp/message?sessionId=abc",
	)
	require.NoError(t, err)
	assert.Equal(t, "http://localhost:8080/mcp/message?sessionId=abc", resolved)
}

// TestResolveEndpointURL_AbsolutePath verifies that an absolute path (e.g.,
// "/mcp/message?sessionId=abc") is resolved against the SSE URL's scheme and
// host. This is the case when a server behind a reverse proxy sends only the
// path portion.
func TestResolveEndpointURL_AbsolutePath(t *testing.T) {
	resolved, err := client.ResolveEndpointURL(
		"http://localhost:8080/api/mcp/sse",
		"/mcp/message?sessionId=abc",
	)
	require.NoError(t, err)
	assert.Equal(t, "http://localhost:8080/mcp/message?sessionId=abc", resolved)
}

// TestResolveEndpointURL_RelativePath verifies that a relative path (e.g.,
// "message?sessionId=abc") is resolved against the SSE URL's directory. For
// "http://host/v2/mcp/sse" + "message?s=abc", the result should be
// "http://host/v2/mcp/message?s=abc".
//
// This is the trickiest case — before the fix, the client would store the bare
// relative string as the POST URL, which would fail on http.NewRequest.
func TestResolveEndpointURL_RelativePath(t *testing.T) {
	resolved, err := client.ResolveEndpointURL(
		"http://localhost:8080/v2/mcp/sse",
		"message?sessionId=abc",
	)
	require.NoError(t, err)
	assert.Equal(t, "http://localhost:8080/v2/mcp/message?sessionId=abc", resolved)
}

// TestResolveEndpointURL_DifferentHost verifies that when the endpoint event
// contains a URL pointing to a different host (e.g., an internal backend), it
// is used as-is. This handles the reverse-proxy scenario where the backend
// advertises its own address.
func TestResolveEndpointURL_DifferentHost(t *testing.T) {
	resolved, err := client.ResolveEndpointURL(
		"https://proxy.example.com/api/mcp/sse",
		"http://backend:8080/mcp/message?sessionId=abc",
	)
	require.NoError(t, err)
	assert.Equal(t, "http://backend:8080/mcp/message?sessionId=abc", resolved)
}

// TestResolveEndpointURL_PreservesQueryParams verifies that all query
// parameters from the endpoint event are preserved in the resolved URL.
func TestResolveEndpointURL_PreservesQueryParams(t *testing.T) {
	resolved, err := client.ResolveEndpointURL(
		"http://localhost:8080/mcp/sse",
		"message?sessionId=abc&token=xyz",
	)
	require.NoError(t, err)
	assert.Equal(t, "http://localhost:8080/mcp/message?sessionId=abc&token=xyz", resolved)
}

// TestResolveEndpointURL_InvalidBase verifies that an unparseable base URL
// returns an error.
func TestResolveEndpointURL_InvalidBase(t *testing.T) {
	_, err := client.ResolveEndpointURL("://invalid", "message")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing SSE URL")
}

// TestResolveEndpointURL_InvalidEndpoint verifies that an unparseable endpoint
// URL returns an error.
func TestResolveEndpointURL_InvalidEndpoint(t *testing.T) {
	_, err := client.ResolveEndpointURL("http://localhost/sse", "://bad")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing endpoint URL")
}

// TestSSE_SessionIDFromQuery verifies that the sessionId is correctly extracted
// from the resolved endpoint URL's query parameters using proper URL parsing,
// not string manipulation.
func TestSSE_SessionIDFromQuery(t *testing.T) {
	c, _ := setupSSEClient(t)

	// SessionID should be non-empty after a successful connect
	sid := c.SessionID()
	assert.NotEmpty(t, sid, "SessionID should be extracted from endpoint URL query")
}

// TestSSE_EndpointURLResolve_Integration verifies end-to-end that the SSE
// client correctly resolves the endpoint URL sent by the real MCP server and
// can successfully make tool calls. This uses the actual SSE transport (not
// mocked) so it validates the full connect → resolve → POST → response flow.
func TestSSE_EndpointURLResolve_Integration(t *testing.T) {
	c, _ := setupSSEClient(t)

	result, err := c.ToolCall("echo", map[string]any{"message": "resolved"})
	require.NoError(t, err)
	assert.Contains(t, result, "resolved")
}
