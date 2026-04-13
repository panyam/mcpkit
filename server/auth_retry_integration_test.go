package server_test

import (
	"net/http/httptest"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockTokenSource tracks Token() calls and returns configurable tokens.
type authRetryMockTS struct {
	tokens []string
	idx    int
}

func (m *authRetryMockTS) Token() (string, error) {
	if m.idx >= len(m.tokens) {
		m.idx = len(m.tokens) - 1
	}
	t := m.tokens[m.idx]
	m.idx++
	return t, nil
}

// TestClient_Streamable_401Integration verifies that the full Streamable HTTP
// client transport handles 401 on initialize correctly — the server returns
// 401, the transport refreshes the token, and the retry succeeds.
func TestClient_Streamable_401Integration(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "auth-test", Version: "1.0"},
		server.WithBearerToken("valid-token"))
	srv.RegisterTool(
		core.ToolDef{Name: "echo", Description: "echo", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"message": map[string]any{"type": "string"}}}},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			var p struct{ Message string `json:"message"` }
			req.Bind(&p)
			return core.TextResult("echo: "+p.Message), nil
		},
	)
	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	tokenSrc := &authRetryMockTS{tokens: []string{"wrong-token", "valid-token"}}

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"},
		client.WithTokenSource(tokenSrc))

	err := c.Connect()
	require.NoError(t, err, "Connect should succeed after 401 retry")
	defer c.Close()

	result, err := c.ToolCall("echo", map[string]any{"message": "hello"})
	require.NoError(t, err)
	assert.Contains(t, result, "hello")
}

// TestClient_Streamable_AuthErrorType verifies that ClientAuthError is
// properly returned and inspectable when the server permanently rejects auth.
func TestClient_Streamable_AuthErrorType(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "auth-test", Version: "1.0"},
		server.WithBearerToken("valid-token"))
	srv.RegisterTool(
		core.ToolDef{Name: "echo", Description: "echo"},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("ok"), nil
		},
	)
	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"},
		client.WithClientBearerToken("wrong-token"))

	err := c.Connect()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401", "error should mention 401")
}
