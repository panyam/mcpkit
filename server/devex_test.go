package server_test

// Tests for developer experience features:
// #71 Server.Run(), #61 Stateless mode, #68 In-memory transport,
// #73 Close sessions, #74 Error codes, #77 Structured error output.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	client "github.com/panyam/mcpkit/client"
	core "github.com/panyam/mcpkit/core"
	server "github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// #74: JSON-RPC Error Codes
// =============================================================================

// TestErrCodeServerError verifies that the ErrCodeServerError constant is in
// the JSON-RPC 2.0 server error range (-32000 to -32099).
func TestErrCodeServerError(t *testing.T) {
	assert.Equal(t, -32000, core.ErrCodeServerError)
	assert.True(t, core.ErrCodeServerError >= -32099 && core.ErrCodeServerError <= -32000,
		"ErrCodeServerError should be in the server error range")
}

// TestMCPErrorCodesOutsideReservedRange verifies that MCP application error
// codes (tool, resource, prompt, completion errors) are outside the JSON-RPC
// 2.0 reserved ranges: -32700 (parse), -32600 to -32603 (standard), and
// -32000 to -32099 (implementation-defined). This prevents collision with
// JSON-RPC protocol errors and keeps MCP semantics separate.
func TestMCPErrorCodesOutsideReservedRange(t *testing.T) {
	mcpCodes := map[string]int{
		"ErrCodeToolExecutionError": core.ErrCodeToolExecutionError,
		"ErrCodeResourceError":      core.ErrCodeResourceError,
		"ErrCodePromptError":        core.ErrCodePromptError,
		"ErrCodeCompletionError":    core.ErrCodeCompletionError,
		"ErrCodeCancelled":          server.ErrCodeCancelled,
	}

	for name, code := range mcpCodes {
		// Must not be in standard JSON-RPC range (-32700, -32600 to -32603)
		assert.NotEqual(t, -32700, code, "%s should not be ErrCodeParse", name)
		assert.False(t, code >= -32603 && code <= -32600, "%s (%d) should not be in standard range", name, code)
		// Must not be in implementation-defined range (-32000 to -32099)
		assert.False(t, code >= -32099 && code <= -32000, "%s (%d) should not be in server error range", name, code)
	}
}

// =============================================================================
// #77: Structured Error Output
// =============================================================================

// TestStructuredResult verifies that StructuredResult creates a ToolResult
// with both text content and structured data.
func TestStructuredResult(t *testing.T) {
	data := map[string]any{"count": 42, "status": "ok"}
	result := core.StructuredResult("42 items found", data)

	assert.False(t, result.IsError)
	require.Len(t, result.Content, 1)
	assert.Equal(t, "42 items found", result.Content[0].Text)
	assert.Equal(t, data, result.StructuredContent)
}

// TestStructuredError verifies that StructuredError creates a ToolResult
// marked as an error with both text and structured error data.
func TestStructuredError(t *testing.T) {
	data := map[string]any{"code": "NOT_FOUND", "resource": "user-123"}
	result := core.StructuredError("user not found", data)

	assert.True(t, result.IsError)
	require.Len(t, result.Content, 1)
	assert.Equal(t, "user not found", result.Content[0].Text)
	assert.Equal(t, data, result.StructuredContent)
}

// TestOutputSchemaOnToolDef verifies that ToolDef supports OutputSchema field
// for tools that produce structured output.
func TestOutputSchemaOnToolDef(t *testing.T) {
	def := core.ToolDef{
		Name:        "search",
		Description: "Search for items",
		InputSchema: map[string]any{"type": "object"},
		OutputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"results": map[string]any{"type": "array"},
				"total":   map[string]any{"type": "integer"},
			},
		},
	}

	// Verify it marshals correctly
	raw, err := json.Marshal(def)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "outputSchema")
	assert.Contains(t, string(raw), "results")
}

// TestStructuredContentInDispatch verifies that StructuredContent flows through
// the dispatch layer correctly — the JSON-RPC response includes structuredContent.
func TestStructuredContentInDispatch(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	srv.RegisterTool(
		core.ToolDef{
			Name:         "structured",
			Description:  "returns structured data",
			InputSchema:  map[string]any{"type": "object"},
			OutputSchema: map[string]any{"type": "object", "properties": map[string]any{"count": map[string]any{"type": "integer"}}},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.StructuredResult("3 items", map[string]any{"count": 3}), nil
		},
	)

	// Initialize
	initReq := &core.Request{ID: json.RawMessage(`1`), Method: "initialize", Params: json.RawMessage(`{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`)}
	srv.Dispatch(context.Background(), initReq)
	srv.Dispatch(context.Background(), &core.Request{Method: "notifications/initialized"})

	// Call tool
	resp := srv.Dispatch(context.Background(), &core.Request{
		ID:     json.RawMessage(`2`),
		Method: "tools/call",
		Params: json.RawMessage(`{"name":"structured"}`),
	})

	require.NotNil(t, resp)
	require.Nil(t, resp.Error)

	// Check that structuredContent is in the response
	var result map[string]any
	require.NoError(t, resp.ResultAs(&result))
	assert.NotNil(t, result["structuredContent"], "response should contain structuredContent")
}

// =============================================================================
// #68: In-Memory Transport
// =============================================================================

// TestInMemoryTransport_Connect verifies that an in-memory client can connect
// to a server without HTTP, perform the initialize handshake, and make tool calls.
func TestInMemoryTransport_Connect(t *testing.T) {
	srv := testutil.NewTestServer()

	c := client.NewClient("memory://", core.ClientInfo{Name: "mem-test", Version: "1.0"}, client.WithTransport(
		server.NewInProcessTransport(srv)),
	)
	require.NoError(t, c.Connect())
	defer c.Close()

	assert.Equal(t, "test-server", c.ServerInfo.Name)
	assert.Equal(t, "memory", c.SessionID())
}

// TestInMemoryTransport_ToolCall verifies that tool calls work correctly
// through the in-memory transport — no HTTP overhead.
func TestInMemoryTransport_ToolCall(t *testing.T) {
	srv := testutil.NewTestServer()

	c := client.NewClient("memory://", core.ClientInfo{Name: "mem-test", Version: "1.0"}, client.WithTransport(
		server.NewInProcessTransport(srv)),
	)
	require.NoError(t, c.Connect())
	defer c.Close()

	result, err := c.ToolCall("echo", map[string]any{"message": "hello from memory"})
	require.NoError(t, err)
	assert.Contains(t, result, "hello from memory")
}

// TestInMemoryTransport_ReadResource verifies that resource reads work
// through the in-memory transport.
func TestInMemoryTransport_ReadResource(t *testing.T) {
	srv := testutil.NewTestServer()

	c := client.NewClient("memory://", core.ClientInfo{Name: "mem-test", Version: "1.0"}, client.WithTransport(
		server.NewInProcessTransport(srv)),
	)
	require.NoError(t, c.Connect())
	defer c.Close()

	content, err := c.ReadResource("test://info")
	require.NoError(t, err)
	assert.Contains(t, content, "hello from test")
}

// TestInMemoryTransport_ListTools verifies that tools/list works through
// the in-memory transport.
func TestInMemoryTransport_ListTools(t *testing.T) {
	srv := testutil.NewTestServer()

	c := client.NewClient("memory://", core.ClientInfo{Name: "mem-test", Version: "1.0"}, client.WithTransport(
		server.NewInProcessTransport(srv)),
	)
	require.NoError(t, c.Connect())
	defer c.Close()

	tools, err := c.ListTools()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(tools), 2, "should have echo + fail tools")
}

// =============================================================================
// #61: Stateless Mode
// =============================================================================

// TestStatelessMode_NoSession verifies that stateless mode handles requests
// without session tracking — no Mcp-Session-Id header, every request uses
// a fresh dispatcher.
func TestStatelessMode_NoSession(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "stateless", Version: "1.0"})
	srv.RegisterTool(
		core.ToolDef{Name: "ping", Description: "ping", InputSchema: map[string]any{"type": "object"}},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("pong"), nil
		},
	)

	handler := srv.Handler(server.WithStreamableHTTP(true), server.WithStateless(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	// Call tools/call directly without initialize — stateless auto-initializes
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ping"}}`
	resp, err := http.Post(ts.URL+"/mcp", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, 200, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("Mcp-Session-Id"), "stateless should not set session ID")

	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	assert.NotNil(t, result["result"], "should return a result")
}

// TestStatelessMode_IndependentRequests verifies that each stateless request
// gets its own dispatcher — no state leaks between requests.
func TestStatelessMode_IndependentRequests(t *testing.T) {
	var callCount int
	srv := server.NewServer(core.ServerInfo{Name: "stateless", Version: "1.0"})
	srv.RegisterTool(
		core.ToolDef{Name: "count", Description: "count", InputSchema: map[string]any{"type": "object"}},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			callCount++
			return core.TextResult(fmt.Sprintf("call-%d", callCount)), nil
		},
	)

	handler := srv.Handler(server.WithStreamableHTTP(true), server.WithStateless(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	// Two independent requests
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"count"}}`
	http.Post(ts.URL+"/mcp", "application/json", strings.NewReader(body))
	http.Post(ts.URL+"/mcp", "application/json", strings.NewReader(body))

	assert.Equal(t, 2, callCount, "both requests should reach the handler")
}

// =============================================================================
// #73: Close Sessions
// =============================================================================

// TestCloseSession verifies that Server.CloseSession terminates an active
// session, making subsequent requests with that session ID fail.
func TestCloseSession(t *testing.T) {
	srv := testutil.NewTestServer()
	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "close-test", Version: "1.0"})
	require.NoError(t, c.Connect())
	sid := c.SessionID()
	require.NotEmpty(t, sid)

	// Close the session from server side
	assert.True(t, srv.CloseSession(sid), "should find and close the session")

	// Subsequent call should fail (session not found)
	_, err := c.ToolCall("echo", map[string]any{"message": "after close"})
	assert.Error(t, err, "call after session close should fail")
	c.Close()
}

// TestCloseAllSessions verifies that Server.CloseAllSessions terminates
// all active sessions.
func TestCloseAllSessions(t *testing.T) {
	srv := testutil.NewTestServer()
	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	// Create two clients
	c1 := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "close-test-1", Version: "1.0"})
	require.NoError(t, c1.Connect())
	c2 := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "close-test-2", Version: "1.0"})
	require.NoError(t, c2.Connect())

	// Close all sessions
	srv.CloseAllSessions()

	// Both clients should fail
	_, err1 := c1.ToolCall("echo", map[string]any{"message": "c1"})
	_, err2 := c2.ToolCall("echo", map[string]any{"message": "c2"})
	assert.Error(t, err1, "c1 should fail after close all")
	assert.Error(t, err2, "c2 should fail after close all")
	c1.Close()
	c2.Close()
}

// TestCloseSession_NotFound verifies that closing a non-existent session
// returns false without error.
func TestCloseSession_NotFound(t *testing.T) {
	srv := testutil.NewTestServer()
	_ = srv.Handler(server.WithStreamableHTTP(true)) // create transport so closers are registered
	assert.False(t, srv.CloseSession("nonexistent-id"))
}

// =============================================================================
// #71: Server.Run() — can't test Run() directly (it blocks), but verify
// that it correctly sets defaults.
// =============================================================================

// TestRunDefaultsToStreamableHTTP moved to server/server_internal_test.go
// (needs access to unexported transportConfig)
