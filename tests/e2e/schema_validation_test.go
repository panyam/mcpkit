package e2e_test

// End-to-end test for server-side JSON Schema validation (#184).
// Unit tests in server/schema_validation_test.go exercise the Dispatch
// path directly; this test exercises the full HTTP wire — Client →
// Streamable HTTP transport → dispatcher → validator → error response —
// and inspects the -32602 error shape over the wire.

import (
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
// spec-compliant -32602 Invalid Params error to the client. The error's
// data field must carry a structured errors list that agents can parse to
// self-correct on the next call.
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
	}, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
		return core.TextResult("ok"), nil
	})

	handler := srv.Handler(server.WithStreamableHTTP(true), server.WithSSE(false))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"})
	require.NoError(t, c.Connect())
	defer c.Close()

	// Call with a negative value — violates minimum: 1
	_, err := c.ToolCall("strict", map[string]any{"count": -5})
	require.Error(t, err, "expected validation error over the wire")

	// The error message format from client.go is "RPC error <code>: <msg>".
	// This locks in both the error code (-32602) and the validation message
	// shape — the two things agents parse to decide what to do next.
	assert.Contains(t, err.Error(), "-32602",
		"validation errors must use -32602 per #184")
	assert.Contains(t, err.Error(), "argument validation failed")
}
