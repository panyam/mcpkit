package main

// Conformance tools implement the tool contracts expected by the official
// MCP conformance test suite (@modelcontextprotocol/conformance).
// See: https://github.com/modelcontextprotocol/conformance

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/panyam/mcpkit"
)

// registerConformanceTools adds all tools required by the MCP conformance suite.
func registerConformanceTools(srv *mcpkit.Server) {
	// test_simple_text: returns a fixed text response (no arguments)
	srv.RegisterTool(
		mcpkit.ToolDef{
			Name:        "test_simple_text",
			Description: "Returns a simple text response for conformance testing",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx context.Context, req mcpkit.ToolRequest) (mcpkit.ToolResult, error) {
			return mcpkit.TextResult("This is a simple text response for testing."), nil
		},
	)

	// test_error_handling: always returns isError: true
	srv.RegisterTool(
		mcpkit.ToolDef{
			Name:        "test_error_handling",
			Description: "Returns an error result for conformance testing",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx context.Context, req mcpkit.ToolRequest) (mcpkit.ToolResult, error) {
			return mcpkit.ToolResult{}, fmt.Errorf("Test error from tool")
		},
	)

	// test_image_content: returns base64 PNG image content
	srv.RegisterTool(
		mcpkit.ToolDef{
			Name:        "test_image_content",
			Description: "Returns image content for conformance testing",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx context.Context, req mcpkit.ToolRequest) (mcpkit.ToolResult, error) {
			// Minimal 1x1 red PNG
			pngBytes := minimalPNG()
			return mcpkit.ToolResult{
				Content: []mcpkit.Content{{
					Type:     "image",
					MimeType: "image/png",
					Data:     base64.StdEncoding.EncodeToString(pngBytes),
				}},
			}, nil
		},
	)

	// test_audio_content: returns base64 audio content
	srv.RegisterTool(
		mcpkit.ToolDef{
			Name:        "test_audio_content",
			Description: "Returns audio content for conformance testing",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx context.Context, req mcpkit.ToolRequest) (mcpkit.ToolResult, error) {
			// Minimal WAV header (44 bytes, no samples)
			wavBytes := minimalWAV()
			return mcpkit.ToolResult{
				Content: []mcpkit.Content{{
					Type:     "audio",
					MimeType: "audio/wav",
					Data:     base64.StdEncoding.EncodeToString(wavBytes),
				}},
			}, nil
		},
	)

	// test_mixed_content: returns text + image content
	srv.RegisterTool(
		mcpkit.ToolDef{
			Name:        "test_multiple_content_types",
			Description: "Returns mixed text and image content for conformance testing",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx context.Context, req mcpkit.ToolRequest) (mcpkit.ToolResult, error) {
			pngBytes := minimalPNG()
			return mcpkit.ToolResult{
				Content: []mcpkit.Content{
					{Type: "text", Text: "Here is an image:"},
					{Type: "image", MimeType: "image/png", Data: base64.StdEncoding.EncodeToString(pngBytes)},
				},
			}, nil
		},
	)

	// test_tool_with_logging: emits 3 log notifications during tool execution.
	// The conformance suite calls this tool after setting the log level to verify
	// that notifications/message events are sent on the transport during execution.
	// Sends 3 info-level log notifications with 50ms delays to test streaming.
	srv.RegisterTool(
		mcpkit.ToolDef{
			Name:        "test_tool_with_logging",
			Description: "Emits log notifications during execution for conformance testing",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx context.Context, req mcpkit.ToolRequest) (mcpkit.ToolResult, error) {
			mcpkit.EmitLog(ctx, mcpkit.LogInfo, "test", "Tool execution started")
			time.Sleep(50 * time.Millisecond)
			mcpkit.EmitLog(ctx, mcpkit.LogInfo, "test", "Tool processing data")
			time.Sleep(50 * time.Millisecond)
			mcpkit.EmitLog(ctx, mcpkit.LogInfo, "test", "Tool execution completed")
			return mcpkit.TextResult("Execution complete"), nil
		},
	)

	// test_embedded_resource: returns a resource content item
	srv.RegisterTool(
		mcpkit.ToolDef{
			Name:        "test_embedded_resource",
			Description: "Returns embedded resource content for conformance testing",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx context.Context, req mcpkit.ToolRequest) (mcpkit.ToolResult, error) {
			return mcpkit.ToolResult{
				Content: []mcpkit.Content{{
					Type: "resource",
					Resource: &mcpkit.ResourceContent{
						URI:      "file:///test/resource.txt",
						MimeType: "text/plain",
						Text:     "This is an embedded resource for testing.",
					},
				}},
			}, nil
		},
	)
}

// minimalPNG returns a valid 1x1 red PNG image (67 bytes).
func minimalPNG() []byte {
	// Pre-computed minimal 1x1 red pixel PNG
	return []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, // PNG signature
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52, // IHDR chunk
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, // 1x1
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xde, // 8-bit RGB
		0x00, 0x00, 0x00, 0x0c, 0x49, 0x44, 0x41, 0x54, // IDAT chunk
		0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0x00, 0x00, // compressed
		0x00, 0x02, 0x00, 0x01, 0xe2, 0x21, 0xbc, 0x33, // red pixel
		0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, // IEND chunk
		0xae, 0x42, 0x60, 0x82,
	}
}

// minimalWAV returns a valid minimal WAV file header (44 bytes, 0 samples).
func minimalWAV() []byte {
	return []byte{
		'R', 'I', 'F', 'F', // ChunkID
		36, 0, 0, 0, // ChunkSize (36 + 0 data bytes)
		'W', 'A', 'V', 'E', // Format
		'f', 'm', 't', ' ', // Subchunk1ID
		16, 0, 0, 0, // Subchunk1Size (PCM)
		1, 0, // AudioFormat (PCM)
		1, 0, // NumChannels (mono)
		0x44, 0xac, 0x00, 0x00, // SampleRate (44100)
		0x88, 0x58, 0x01, 0x00, // ByteRate (88200)
		2, 0, // BlockAlign
		16, 0, // BitsPerSample
		'd', 'a', 't', 'a', // Subchunk2ID
		0, 0, 0, 0, // Subchunk2Size (0 samples)
	}
}
