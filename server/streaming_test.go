package server_test

import (
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

// TestStreamingToolResultsOverSSE verifies that a tool handler can emit
// partial content blocks during execution via core.EmitContent, and that
// the client receives them as streaming chunks before the final result.
// This uses the Streamable HTTP transport with SSE streaming (Accept:
// text/event-stream), which is the primary delivery mechanism for
// incremental tool results.
func TestStreamingToolResultsOverSSE(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	srv.Register(server.Tool{
		ToolDef: core.ToolDef{
			Name:        "stream-test",
			Description: "Emits 3 content chunks then returns final result",
			InputSchema: map[string]any{"type": "object"},
		},
		Handler: func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			for i := 1; i <= 3; i++ {
				core.EmitContent(ctx, req.RequestID, core.Content{
					Type: "text",
					Text: "chunk-" + string(rune('0'+i)),
				})
				time.Sleep(10 * time.Millisecond) // simulate work
			}
			return core.TextResult("final result"), nil
		},
	})

	handler := srv.Handler(server.WithStreamableHTTP(true), server.WithSSE(false))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	var chunks []core.ContentChunk
	var mu sync.Mutex

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"},
		client.WithGetSSEStream(),
		client.WithContentChunkHandler(func(chunk core.ContentChunk) {
			mu.Lock()
			chunks = append(chunks, chunk)
			mu.Unlock()
		}),
	)
	require.NoError(t, c.Connect())
	defer c.Close()

	// Call the streaming tool
	result, err := c.ToolCall("stream-test", nil)
	require.NoError(t, err)
	assert.Contains(t, result, "final result")

	// Give SSE a moment to deliver all chunks
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.GreaterOrEqual(t, len(chunks), 1, "should receive at least one content chunk")
	for _, chunk := range chunks {
		assert.Equal(t, "text", chunk.Content.Type)
		assert.Contains(t, chunk.Content.Text, "chunk-")
	}
}

// TestStreamingToolErrorMidStream verifies that when a tool emits some
// content chunks then returns an error result, the client receives the
// partial chunks AND the final error result. The chunks are a preview;
// the final result (with isError) is authoritative.
func TestStreamingToolErrorMidStream(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	srv.Register(server.Tool{
		ToolDef: core.ToolDef{
			Name:        "stream-fail",
			Description: "Emits chunks then fails",
			InputSchema: map[string]any{"type": "object"},
		},
		Handler: func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			core.EmitContent(ctx, req.RequestID, core.Content{Type: "text", Text: "partial-1"})
			core.EmitContent(ctx, req.RequestID, core.Content{Type: "text", Text: "partial-2"})
			return core.ErrorResult("failed mid-stream"), nil
		},
	})

	handler := srv.Handler(server.WithStreamableHTTP(true), server.WithSSE(false))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	chunkCount := 0
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"},
		client.WithGetSSEStream(),
		client.WithContentChunkHandler(func(chunk core.ContentChunk) {
			chunkCount++
		}),
	)
	require.NoError(t, c.Connect())
	defer c.Close()

	// Tool returns error — ToolCall wraps isError as Go error
	_, err := c.ToolCall("stream-fail", nil)
	assert.Error(t, err, "should propagate tool error")
	assert.Contains(t, err.Error(), "failed mid-stream")

	time.Sleep(100 * time.Millisecond)
	assert.GreaterOrEqual(t, chunkCount, 1, "should receive chunks even when tool fails")
}

// TestStreamingToolNoHandler verifies that when no content chunk handler
// is configured on the client, streaming chunks are silently ignored and
// the final ToolResult is still returned correctly.
func TestStreamingToolNoHandler(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	srv.Register(server.Tool{
		ToolDef: core.ToolDef{
			Name:        "stream-ignored",
			Description: "Emits chunks but client ignores them",
			InputSchema: map[string]any{"type": "object"},
		},
		Handler: func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			core.EmitContent(ctx, req.RequestID, core.Content{Type: "text", Text: "ignored"})
			return core.TextResult("final only"), nil
		},
	})

	handler := srv.Handler(server.WithStreamableHTTP(true), server.WithSSE(false))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"})
	require.NoError(t, c.Connect())
	defer c.Close()

	result, err := c.ToolCall("stream-ignored", nil)
	require.NoError(t, err)
	assert.Contains(t, result, "final only")
}

// TestStreamingToolCustomMethod verifies that when the server is configured
// with a custom content chunk method via WithContentChunkMethod, the chunks
// are delivered using that method name.
func TestStreamingToolCustomMethod(t *testing.T) {
	srv := server.NewServer(
		core.ServerInfo{Name: "test", Version: "1.0"},
		server.WithContentChunkMethod("custom/streaming"),
	)
	srv.Register(server.Tool{
		ToolDef: core.ToolDef{
			Name:        "custom-method",
			Description: "Uses custom chunk method",
			InputSchema: map[string]any{"type": "object"},
		},
		Handler: func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			core.EmitContent(ctx, req.RequestID, core.Content{Type: "text", Text: "custom chunk"})
			return core.TextResult("done"), nil
		},
	})

	handler := srv.Handler(server.WithStreamableHTTP(true), server.WithSSE(false))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	// Use generic notification callback to verify the custom method name
	var receivedMethod string
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"},
		client.WithGetSSEStream(),
		client.WithNotificationCallback(func(method string, params any) {
			if receivedMethod == "" {
				receivedMethod = method
			}
		}),
	)
	require.NoError(t, c.Connect())
	defer c.Close()

	c.ToolCall("custom-method", nil)
	time.Sleep(100 * time.Millisecond)

	assert.Equal(t, "custom/streaming", receivedMethod, "should use custom method name")
}
