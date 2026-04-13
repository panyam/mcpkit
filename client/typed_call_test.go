package client_test

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestToolCallTyped verifies that ToolCallTyped[T] correctly unmarshals
// structured tool output into a caller-specified Go type. This is the
// typed counterpart to ToolCall (which returns text only).
func TestToolCallTyped(t *testing.T) {
	type SearchResult struct {
		Items []string `json:"items"`
		Total int      `json:"total"`
	}

	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	srv.RegisterTool(
		core.ToolDef{
			Name:         "search",
			Description:  "Returns structured results",
			InputSchema:  map[string]any{"type": "object"},
			OutputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.StructuredResult("found 2 items", SearchResult{
				Items: []string{"a", "b"},
				Total: 2,
			}), nil
		},
	)

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"})
	require.NoError(t, c.Connect())
	defer c.Close()

	result, err := client.ToolCallTyped[SearchResult](c, "search", nil)
	require.NoError(t, err)
	assert.Equal(t, 2, result.Total)
	assert.Equal(t, []string{"a", "b"}, result.Items)
}

// TestToolCallTypedNoStructuredContent verifies that ToolCallTyped returns
// a clear error when the tool returns text-only content without structured
// output (no OutputSchema/StructuredContent).
func TestToolCallTypedNoStructuredContent(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	srv.RegisterTool(
		core.ToolDef{
			Name:        "text-only",
			Description: "Returns text only",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("just text"), nil
		},
	)

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"})
	require.NoError(t, c.Connect())
	defer c.Close()

	type Anything struct{ X int }
	_, err := client.ToolCallTyped[Anything](c, "text-only", nil)
	assert.Error(t, err, "should error when no structured content")
	assert.Contains(t, err.Error(), "no structured content")
}

// TestToolCallTypedToolError verifies that ToolCallTyped propagates tool
// errors (isError: true) as Go errors, not as unmarshaling failures.
func TestToolCallTypedToolError(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	srv.RegisterTool(
		core.ToolDef{
			Name:        "fail",
			Description: "Always fails",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.ErrorResult("something went wrong"), nil
		},
	)

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"})
	require.NoError(t, c.Connect())
	defer c.Close()

	type Anything struct{ X int }
	_, err := client.ToolCallTyped[Anything](c, "fail", nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "tool error")
}

// TestToolCallFullSuccess verifies that ToolCallFull returns the complete
// ToolResult on success, preserving all content blocks.
func TestToolCallFullSuccess(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	srv.RegisterTool(
		core.ToolDef{
			Name:        "greet",
			Description: "Returns a greeting",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("hello world"), nil
		},
	)

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"})
	require.NoError(t, c.Connect())
	defer c.Close()

	result, err := c.ToolCallFull("greet", nil)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	require.Len(t, result.Content, 1)
	assert.Equal(t, "hello world", result.Content[0].Text)
}

// TestToolCallFullError verifies that ToolCallFull returns tool-level errors
// in the result (IsError: true) instead of as Go errors — preserving
// structured error data that ToolCall would flatten.
func TestToolCallFullError(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	srv.RegisterTool(
		core.ToolDef{
			Name:        "conflict",
			Description: "Returns a structured error",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.StructuredError("version conflict", map[string]any{
				"error":           "version_conflict",
				"current_version": 5,
			}), nil
		},
	)

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"})
	require.NoError(t, c.Connect())
	defer c.Close()

	// ToolCallFull should NOT return a Go error for tool-level errors.
	result, err := c.ToolCallFull("conflict", nil)
	require.NoError(t, err, "transport should succeed even when tool errors")
	assert.True(t, result.IsError)
	require.Len(t, result.Content, 1)
	assert.Equal(t, "version conflict", result.Content[0].Text)

	// StructuredContent should be preserved (not flattened).
	require.NotNil(t, result.StructuredContent)
	raw, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)
	var conflict map[string]any
	require.NoError(t, json.Unmarshal(raw, &conflict))
	assert.Equal(t, "version_conflict", conflict["error"])
	assert.Equal(t, float64(5), conflict["current_version"])

	// Contrast: ToolCall would flatten this to a Go error, losing the data.
	_, toolCallErr := c.ToolCall("conflict", nil)
	assert.Error(t, toolCallErr, "ToolCall should return Go error for isError:true")
	assert.Contains(t, toolCallErr.Error(), "version conflict")
}

// TestToolCallFullStructuredSuccess verifies that ToolCallFull preserves
// structured content on successful results.
func TestToolCallFullStructuredSuccess(t *testing.T) {
	type SearchResult struct {
		Items []string `json:"items"`
		Total int      `json:"total"`
	}

	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	srv.RegisterTool(
		core.ToolDef{
			Name:         "search",
			Description:  "Returns structured results",
			InputSchema:  map[string]any{"type": "object"},
			OutputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.StructuredResult("found 2 items", SearchResult{
				Items: []string{"a", "b"},
				Total: 2,
			}), nil
		},
	)

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"})
	require.NoError(t, c.Connect())
	defer c.Close()

	result, err := c.ToolCallFull("search", nil)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	require.Len(t, result.Content, 1)
	assert.Equal(t, "found 2 items", result.Content[0].Text)
	require.NotNil(t, result.StructuredContent)
}
