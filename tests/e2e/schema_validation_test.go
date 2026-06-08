package e2e_test

// End-to-end test for server-side JSON Schema validation (#184).
// Unit tests in server/schema_validation_test.go exercise the Dispatch
// path directly; this test exercises the full HTTP wire — Client →
// Streamable HTTP transport → dispatcher → validator → error response —
// and inspects the isError-tool-result shape over the wire.

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/panyam/mcpkit/client"
	core "github.com/panyam/mcpkit/core"
	server "github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2E_SchemaValidationErrorOverWire verifies that a tool declared with
// an InputSchema rejects invalid arguments at the server and returns a
// successful JSON-RPC response carrying a tool result with isError: true.
// Wrapping (rather than emitting -32602) matches upstream's ext-apps SDK
// behavior so apps/compat hosts (basic-host, the iframe) can surface
// validation failures through their normal result-rendering UI instead of
// treating them as protocol-level faults.
func TestE2E_SchemaValidationErrorOverWire(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "schema-e2e", Version: "1.0"})
	srv.RegisterTool(core.ToolDef{
		Name:        "strict",
		Description: "A tool that requires a positive integer",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"count"},
			"properties": map[string]any{
				"count": map[string]any{
					"type":    "integer",
					"minimum": 1,
				},
			},
		},
	}, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		return core.TextResult("ok"), nil
	})

	handler := srv.Handler(server.WithStreamableHTTP(true), server.WithSSE(false))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"})
	require.NoError(t, c.Connect())
	defer c.Close()

	// Call with a negative value — violates minimum: 1.
	// Use the lower-level Call so we can inspect the raw tool result;
	// ToolCall would unwrap the isError into a Go error and we'd lose the
	// structured-content payload.
	result, err := c.Call("tools/call", map[string]any{
		"name":      "strict",
		"arguments": map[string]any{"count": -5},
	})
	require.NoError(t, err, "expected isError tool result, got JSON-RPC error")
	require.NotNil(t, result)

	var tr core.ToolResult
	require.NoError(t, json.Unmarshal(result.Raw, &tr))
	assert.True(t, tr.IsError, "expected isError: true on the tool result")
	require.NotEmpty(t, tr.Content, "expected at least one content block")
	assert.Contains(t, tr.Content[0].Text, "Invalid arguments",
		"text content should describe what went wrong")
	require.NotNil(t, tr.StructuredContent,
		"structured payload should carry the validation errors for programmatic consumers")
}
