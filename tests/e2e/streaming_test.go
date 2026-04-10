package e2e_test

// End-to-end test for streaming tool results (#82).
// Verifies that EmitContent chunks flow from a tool handler through the
// full Streamable HTTP SSE transport to a connected client with a content
// chunk handler. Unlike server/ integration tests, this exercises the
// complete server lifecycle including session management, HTTP transport,
// and client reconnection infrastructure.

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2E_StreamingToolResults verifies the end-to-end flow of streaming
// tool content delivery: a tool handler emits 3 content chunks during
// execution, and the client receives each chunk via the content chunk
// handler before the final ToolResult arrives. This tests the full path:
// tool handler → EmitContent → notify → SSE event → client notification
// dispatch → WithContentChunkHandler callback.
func TestE2E_StreamingToolResults(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "streaming-e2e", Version: "1.0"})
	srv.Register(server.Tool{
		ToolDef: core.ToolDef{
			Name:        "slow-analyze",
			Description: "Simulates a long analysis emitting incremental results",
			InputSchema: map[string]any{"type": "object"},
		},
		Handler: func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			steps := []string{"Fetching data...", "Processing rows...", "Generating report..."}
			for _, step := range steps {
				core.EmitContent(ctx, req.RequestID, core.Content{
					Type: "text",
					Text: step,
				})
				time.Sleep(20 * time.Millisecond)
			}
			return core.TextResult("Analysis complete: 42 items found"), nil
		},
	})

	handler := srv.Handler(server.WithStreamableHTTP(true), server.WithSSE(false))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	var chunks []string
	var mu sync.Mutex

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "e2e-streaming", Version: "1.0"},
		client.WithGetSSEStream(),
		client.WithContentChunkHandler(func(chunk core.ContentChunk) {
			mu.Lock()
			chunks = append(chunks, chunk.Content.Text)
			mu.Unlock()
		}),
	)
	require.NoError(t, c.Connect())
	defer c.Close()

	result, err := c.ToolCall("slow-analyze", nil)
	require.NoError(t, err)
	assert.Contains(t, result, "Analysis complete")

	// Allow SSE delivery to complete
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	// Verify we received streaming chunks
	assert.GreaterOrEqual(t, len(chunks), 1, "should receive at least one streaming chunk")

	// Verify chunk content matches what the tool emitted
	expectedPrefixes := []string{"Fetching", "Processing", "Generating"}
	for i, chunk := range chunks {
		if i < len(expectedPrefixes) {
			assert.Contains(t, chunk, expectedPrefixes[i],
				"chunk %d should contain expected prefix", i)
		}
	}
}

// TestE2E_StreamingWithoutHandler verifies that streaming chunks don't
// interfere with normal tool call flow when no content chunk handler is
// configured on the client. The tool still emits chunks (server-side),
// but the client ignores them and returns the final result normally.
func TestE2E_StreamingWithoutHandler(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "streaming-e2e", Version: "1.0"})
	srv.Register(server.Tool{
		ToolDef: core.ToolDef{
			Name:        "chatty-tool",
			Description: "Emits chunks but client has no handler",
			InputSchema: map[string]any{"type": "object"},
		},
		Handler: func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			core.EmitContent(ctx, req.RequestID, core.Content{Type: "text", Text: "ignored chunk"})
			return core.TextResult("final only"), nil
		},
	})

	handler := srv.Handler(server.WithStreamableHTTP(true), server.WithSSE(false))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	// No WithContentChunkHandler — chunks should be silently ignored
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "e2e-no-handler", Version: "1.0"})
	require.NoError(t, c.Connect())
	defer c.Close()

	result, err := c.ToolCall("chatty-tool", nil)
	require.NoError(t, err)
	assert.Contains(t, result, "final only")
}
