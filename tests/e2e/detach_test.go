package e2e_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	client "github.com/panyam/mcpkit/client"
	core "github.com/panyam/mcpkit/core"
	server "github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2E_DetachFromClient_SurvivesPerToolTimeout is the primary e2e test
// for #203. It exercises the full pipeline:
//
//	client.ToolCall → Streamable HTTP POST → server dispatch with
//	ToolDef.Timeout(100ms) → handler calls DetachFromClient →
//	handler runs for 300ms → result returned to client
//
// Without DetachFromClient, the tool would be cancelled at 100ms and
// the client would receive a deadline-exceeded error. With it, the tool
// completes normally and the client gets the correct result.
//
// This proves DetachFromClient works end-to-end through the transport
// layer, dispatch pipeline, middleware chain, and client library — not
// just in unit-test isolation.
func TestE2E_DetachFromClient_SurvivesPerToolTimeout(t *testing.T) {
	srv := server.NewServer(
		core.ServerInfo{Name: "detach-e2e", Version: "0.1.0"},
	)
	srv.RegisterTool(
		core.ToolDef{
			Name:        "detached_long_work",
			Description: "Detaches from client context, runs past its timeout",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"tag": map[string]any{"type": "string"},
			}},
			Timeout: 100 * time.Millisecond, // short timeout
		},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			var args struct {
				Tag string `json:"tag"`
			}
			req.Bind(&args)

			// Detach — strips the 100ms per-tool timeout.
			ctx = core.DetachFromClient(ctx)

			// Simulate long work (300ms — 3x the timeout).
			for i := 0; i < 6; i++ {
				select {
				case <-ctx.Done():
					return core.ErrorResult("cancelled: " + ctx.Err().Error()), nil
				case <-time.After(50 * time.Millisecond):
				}
			}

			return core.TextResult("completed:" + args.Tag), nil
		},
	)

	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true), server.WithSSE(false)))
	t.Cleanup(ts.Close)

	c := client.NewClient(
		ts.URL+"/mcp",
		core.ClientInfo{Name: "detach-e2e-client", Version: "0.1.0"},
	)
	err := c.Connect()
	require.NoError(t, err)
	defer c.Close()

	// Call the tool — it takes 300ms but has a 100ms timeout.
	// Without DetachFromClient this would fail with deadline exceeded.
	result, err := c.ToolCall("detached_long_work", map[string]any{"tag": "e2e-ok"})
	require.NoError(t, err, "detached tool should complete despite timeout")
	assert.Equal(t, "completed:e2e-ok", result)
}

// TestE2E_DetachFromClient_NonDetachedToolCancelledByTimeout is the
// control test: without DetachFromClient, a tool that exceeds its
// per-tool Timeout IS cancelled and the client gets an error response.
// This confirms the default behavior is preserved at the e2e level.
func TestE2E_DetachFromClient_NonDetachedToolCancelledByTimeout(t *testing.T) {
	srv := server.NewServer(
		core.ServerInfo{Name: "no-detach-e2e", Version: "0.1.0"},
	)
	srv.RegisterTool(
		core.ToolDef{
			Name:        "normal_long_work",
			Description: "Does NOT detach — should fail on timeout",
			InputSchema: map[string]any{"type": "object"},
			Timeout:     100 * time.Millisecond,
		},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			// NO DetachFromClient — tool uses the request context.
			for i := 0; i < 6; i++ {
				select {
				case <-ctx.Done():
					return core.ErrorResult("cancelled: " + ctx.Err().Error()), nil
				case <-time.After(50 * time.Millisecond):
				}
			}
			return core.TextResult("should-not-reach"), nil
		},
	)

	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true), server.WithSSE(false)))
	t.Cleanup(ts.Close)

	c := client.NewClient(
		ts.URL+"/mcp",
		core.ClientInfo{Name: "no-detach-e2e-client", Version: "0.1.0"},
	)
	err := c.Connect()
	require.NoError(t, err)
	defer c.Close()

	// Call should fail with a deadline/cancellation error. The server returns
	// a JSON-RPC error (ErrCodeToolExecutionError) when the tool's context
	// is cancelled by the per-tool timeout, and the client wraps it as an error.
	_, err = c.ToolCall("normal_long_work", map[string]any{})
	require.Error(t, err, "non-detached tool should fail on timeout")
	assert.Contains(t, err.Error(), "cancelled", "error should mention cancellation")
}

// TestE2E_DetachFromClient_WithRetryHint verifies the combined pattern
// recommended in the docs: EmitSSERetry + DetachFromClient. The hint is
// emitted before detach (while the connection is live), and the tool
// completes after detach. The tool result is returned on the POST
// response since Streamable HTTP POST is synchronous.
func TestE2E_DetachFromClient_WithRetryHint(t *testing.T) {
	srv := server.NewServer(
		core.ServerInfo{Name: "combo-e2e", Version: "0.1.0"},
	)
	srv.RegisterTool(
		core.ToolDef{
			Name:        "hint_and_detach",
			Description: "Emits retry hint, detaches, then completes",
			InputSchema: map[string]any{"type": "object"},
			Timeout:     50 * time.Millisecond,
		},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			// Emit retry hint while still attached.
			_ = core.EmitSSERetry(ctx, 30*time.Second)

			// Detach to survive the 50ms timeout.
			ctx = core.DetachFromClient(ctx)

			time.Sleep(150 * time.Millisecond) // outlives timeout
			return core.TextResult("combo-done"), nil
		},
	)

	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true), server.WithSSE(false)))
	t.Cleanup(ts.Close)

	c := client.NewClient(
		ts.URL+"/mcp",
		core.ClientInfo{Name: "combo-e2e-client", Version: "0.1.0"},
	)
	err := c.Connect()
	require.NoError(t, err)
	defer c.Close()

	result, err := c.ToolCall("hint_and_detach", map[string]any{})
	require.NoError(t, err)
	assert.Equal(t, "combo-done", result)
}
