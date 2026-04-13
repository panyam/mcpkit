package server_test

import (
	"context"
	"testing"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPerToolTimeout verifies that a per-tool timeout set on ToolDef.Timeout
// applies to that specific tool, causing context cancellation when the tool
// exceeds its configured duration. Other tools without a per-handler timeout
// are unaffected.
func TestPerToolTimeout(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})

	// Slow tool with a 50ms per-handler timeout
	srv.RegisterTool(
		core.ToolDef{
			Name:        "slow",
			Description: "Takes too long",
			InputSchema: map[string]any{"type": "object"},
			Timeout:     50 * time.Millisecond,
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			select {
			case <-ctx.Done():
				return core.ErrorResult("timed out"), ctx.Err()
			case <-time.After(5 * time.Second):
				return core.TextResult("completed"), nil
			}
		},
	)

	// Fast tool with no per-handler timeout
	srv.RegisterTool(
		core.ToolDef{
			Name:        "fast",
			Description: "Returns immediately",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("fast result"), nil
		},
	)

	testutil.InitHandshake(srv)

	// Slow tool should be cancelled by per-handler timeout
	start := time.Now()
	result := srv.Dispatch(context.Background(), testutil.ToolCallRequest("slow", nil))
	elapsed := time.Since(start)
	require.NotNil(t, result)
	// Should complete quickly (timeout fires), not after 5 seconds
	assert.Less(t, elapsed, 1*time.Second, "slow tool should be cancelled by per-handler timeout")

	// Fast tool should work normally
	result = srv.Dispatch(context.Background(), testutil.ToolCallRequest("fast", nil))
	require.NotNil(t, result)
	require.Nil(t, result.Error)
}

// TestPerResourceTimeout verifies that a per-resource timeout set on
// ResourceDef.Timeout applies to resources/read for that specific resource.
func TestPerResourceTimeout(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})

	srv.RegisterResource(
		core.ResourceDef{
			URI:      "test://slow",
			Name:     "Slow Resource",
			MimeType: "text/plain",
			Timeout:  50 * time.Millisecond,
		},
		func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			select {
			case <-ctx.Done():
				return core.ResourceResult{}, ctx.Err()
			case <-time.After(5 * time.Second):
				return core.ResourceResult{Contents: []core.ResourceReadContent{{
					URI: req.URI, MimeType: "text/plain", Text: "slow",
				}}}, nil
			}
		},
	)

	testutil.InitHandshake(srv)

	start := time.Now()
	result := srv.Dispatch(context.Background(), testutil.ResourceReadRequest("test://slow"))
	elapsed := time.Since(start)
	require.NotNil(t, result)
	// Should be an error (timeout) not a successful result after 5s
	assert.Less(t, elapsed, 1*time.Second, "slow resource should be cancelled by per-handler timeout")
	require.NotNil(t, result.Error, "should return error for timed-out resource")
}

// TestPerPromptTimeout verifies that a per-prompt timeout set on
// PromptDef.Timeout applies to prompts/get for that specific prompt.
func TestPerPromptTimeout(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})

	srv.RegisterPrompt(
		core.PromptDef{
			Name:        "slow-prompt",
			Description: "Takes too long",
			Timeout:     50 * time.Millisecond,
		},
		func(ctx core.PromptContext, req core.PromptRequest) (core.PromptResult, error) {
			select {
			case <-ctx.Done():
				return core.PromptResult{}, ctx.Err()
			case <-time.After(5 * time.Second):
				return core.PromptResult{}, nil
			}
		},
	)

	testutil.InitHandshake(srv)

	start := time.Now()
	result := srv.Dispatch(context.Background(), testutil.PromptGetRequest("slow-prompt", nil))
	elapsed := time.Since(start)
	require.NotNil(t, result)
	assert.Less(t, elapsed, 1*time.Second, "slow prompt should be cancelled by per-handler timeout")
	require.NotNil(t, result.Error, "should return error for timed-out prompt")
}

// TestServerWideTimeoutIsFallback verifies that the server-wide
// WithToolTimeout is still used as a fallback when no per-handler timeout
// is set on the ToolDef.
func TestServerWideTimeoutIsFallback(t *testing.T) {
	srv := server.NewServer(
		core.ServerInfo{Name: "test", Version: "1.0"},
		server.WithToolTimeout(50*time.Millisecond),
	)

	srv.RegisterTool(
		core.ToolDef{
			Name:        "slow-no-override",
			Description: "No per-handler timeout, falls back to server-wide",
			InputSchema: map[string]any{"type": "object"},
			// No Timeout set — uses server-wide
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			select {
			case <-ctx.Done():
				return core.ErrorResult("timed out"), ctx.Err()
			case <-time.After(5 * time.Second):
				return core.TextResult("completed"), nil
			}
		},
	)

	testutil.InitHandshake(srv)

	start := time.Now()
	srv.Dispatch(context.Background(), testutil.ToolCallRequest("slow-no-override", nil))
	elapsed := time.Since(start)
	assert.Less(t, elapsed, 1*time.Second, "server-wide timeout should apply as fallback")
}
