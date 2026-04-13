package client_test

import (
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
