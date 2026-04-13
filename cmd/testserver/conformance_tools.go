package main

// Conformance tools implement the tool contracts expected by the official
// MCP conformance test suite (@modelcontextprotocol/conformance).
// See: https://github.com/modelcontextprotocol/conformance

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// registerConformanceTools adds all tools required by the MCP conformance suite.
func registerConformanceTools(srv *server.Server) {
	// test_simple_text: returns a fixed text response (no arguments)
	srv.RegisterTool(
		core.ToolDef{
			Name:        "test_simple_text",
			Description: "Returns a simple text response for conformance testing",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("This is a simple text response for testing."), nil
		},
	)

	// test_error_handling: always returns isError: true
	srv.RegisterTool(
		core.ToolDef{
			Name:        "test_error_handling",
			Description: "Returns an error result for conformance testing",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.ToolResult{}, fmt.Errorf("Test error from tool")
		},
	)

	// test_image_content: returns base64 PNG image content
	srv.RegisterTool(
		core.ToolDef{
			Name:        "test_image_content",
			Description: "Returns image content for conformance testing",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			// Minimal 1x1 red PNG
			pngBytes := minimalPNG()
			return core.ToolResult{
				Content: []core.Content{{
					Type:     "image",
					MimeType: "image/png",
					Data:     base64.StdEncoding.EncodeToString(pngBytes),
				}},
			}, nil
		},
	)

	// test_audio_content: returns base64 audio content
	srv.RegisterTool(
		core.ToolDef{
			Name:        "test_audio_content",
			Description: "Returns audio content for conformance testing",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			// Minimal WAV header (44 bytes, no samples)
			wavBytes := minimalWAV()
			return core.ToolResult{
				Content: []core.Content{{
					Type:     "audio",
					MimeType: "audio/wav",
					Data:     base64.StdEncoding.EncodeToString(wavBytes),
				}},
			}, nil
		},
	)

	// test_multiple_content_types: returns text + image + embedded resource content
	srv.RegisterTool(
		core.ToolDef{
			Name:        "test_multiple_content_types",
			Description: "Returns mixed text, image, and resource content for conformance testing",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			pngBytes := minimalPNG()
			return core.ToolResult{
				Content: []core.Content{
					{Type: "text", Text: "Here is an image:"},
					{Type: "image", MimeType: "image/png", Data: base64.StdEncoding.EncodeToString(pngBytes)},
					{Type: "resource", Resource: &core.ResourceContent{
						URI:      "test://mixed/resource",
						MimeType: "text/plain",
						Text:     "This is an embedded resource in mixed content.",
					}},
				},
			}, nil
		},
	)

	// test_tool_with_logging: emits 3 log notifications during tool execution.
	// The conformance suite calls this tool after setting the log level to verify
	// that notifications/message events are sent on the transport during execution.
	// Sends 3 info-level log notifications with 50ms delays to test streaming.
	srv.RegisterTool(
		core.ToolDef{
			Name:        "test_tool_with_logging",
			Description: "Emits log notifications during execution for conformance testing",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			core.EmitLog(ctx, core.LogInfo, "test", "Tool execution started")
			time.Sleep(50 * time.Millisecond)
			core.EmitLog(ctx, core.LogInfo, "test", "Tool processing data")
			time.Sleep(50 * time.Millisecond)
			core.EmitLog(ctx, core.LogInfo, "test", "Tool execution completed")
			return core.TextResult("Execution complete"), nil
		},
	)

	// test_tool_with_progress: emits 3 progress notifications during tool execution.
	// The conformance suite calls this tool with _meta.progressToken and verifies
	// that notifications/progress events arrive with monotonically increasing progress.
	// Sends progress at 0/100, 50/100, 100/100 with 50ms delays between them.
	srv.RegisterTool(
		core.ToolDef{
			Name:        "test_tool_with_progress",
			Description: "Emits progress notifications during execution for conformance testing",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			core.EmitProgress(ctx, req.ProgressToken, 0, 100, "Starting")
			time.Sleep(50 * time.Millisecond)
			core.EmitProgress(ctx, req.ProgressToken, 50, 100, "Processing")
			time.Sleep(50 * time.Millisecond)
			core.EmitProgress(ctx, req.ProgressToken, 100, 100, "Complete")
			return core.TextResult("Progress complete"), nil
		},
	)

	// test_embedded_resource: returns a resource content item
	srv.RegisterTool(
		core.ToolDef{
			Name:        "test_embedded_resource",
			Description: "Returns embedded resource content for conformance testing",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.ToolResult{
				Content: []core.Content{{
					Type: "resource",
					Resource: &core.ResourceContent{
						URI:      "file:///test/resource.txt",
						MimeType: "text/plain",
						Text:     "This is an embedded resource for testing.",
					},
				}},
			}, nil
		},
	)

	// test_sampling: calls sampling/createMessage during tool execution.
	// The conformance suite's client must respond to the server-to-client request
	// with an LLM inference result. The tool returns the model's response text.
	srv.RegisterTool(
		core.ToolDef{
			Name:        "test_sampling",
			Description: "Calls sampling/createMessage and returns the LLM response for conformance testing",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			result, err := core.Sample(ctx, core.CreateMessageRequest{
				Messages: []core.SamplingMessage{{
					Role:    "user",
					Content: core.Content{Type: "text", Text: "What is the capital of France?"},
				}},
				MaxTokens: 100,
			})
			if err != nil {
				return core.ErrorResult(fmt.Sprintf("sampling failed: %v", err)), nil
			}
			return core.TextResult(fmt.Sprintf("model=%s role=%s text=%s", result.Model, result.Role, result.Content.Text)), nil
		},
	)

	// test_elicitation: calls elicitation/create during tool execution.
	// The conformance suite's client must respond to the server-to-client request
	// with user input. The tool returns the user's action and content.
	srv.RegisterTool(
		core.ToolDef{
			Name:        "test_elicitation",
			Description: "Calls elicitation/create and returns user input for conformance testing",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			result, err := core.Elicit(ctx, core.ElicitationRequest{
				Message:         "Please provide your name",
				RequestedSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"Your name"}}}`),
			})
			if err != nil {
				return core.ErrorResult(fmt.Sprintf("elicitation failed: %v", err)), nil
			}
			if result.Action == "accept" {
				name, _ := result.Content["name"].(string)
				return core.TextResult(fmt.Sprintf("action=accept name=%s", name)), nil
			}
			return core.TextResult(fmt.Sprintf("action=%s", result.Action)), nil
		},
	)

	// test_elicitation_sep1034_defaults: calls elicitation/create with a schema
	// containing default values for all primitive types (SEP-1034 conformance).
	// Schema includes: string, integer, number, enum with default, and boolean,
	// each with a default value set.
	srv.RegisterTool(
		core.ToolDef{
			Name:        "test_elicitation_sep1034_defaults",
			Description: "Calls elicitation/create with default values for all primitive types (SEP-1034)",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			schema := json.RawMessage(`{
				"type": "object",
				"properties": {
					"name":     {"type": "string",  "default": "John Doe"},
					"age":      {"type": "integer", "default": 30},
					"score":    {"type": "number",  "default": 95.5},
					"status":   {"type": "string",  "enum": ["active", "inactive", "pending"], "default": "active"},
					"verified": {"type": "boolean", "default": true}
				}
			}`)
			result, err := core.Elicit(ctx, core.ElicitationRequest{
				Message:         "Please provide your information",
				RequestedSchema: schema,
			})
			if err != nil {
				return core.ErrorResult(fmt.Sprintf("elicitation failed: %v", err)), nil
			}
			contentJSON, _ := json.Marshal(result.Content)
			return core.TextResult(fmt.Sprintf("Elicitation completed: action=%s, content=%s", result.Action, string(contentJSON))), nil
		},
	)

	// test_elicitation_sep1330_enums: calls elicitation/create with all 5 enum
	// variants defined in SEP-1330 conformance: untitled single-select, titled
	// single-select (oneOf), legacy titled (enumNames), untitled multi-select,
	// and titled multi-select (anyOf).
	srv.RegisterTool(
		core.ToolDef{
			Name:        "test_elicitation_sep1330_enums",
			Description: "Calls elicitation/create with all 5 enum variants (SEP-1330)",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			schema := json.RawMessage(`{
				"type": "object",
				"properties": {
					"untitledSingle": {
						"type": "string",
						"enum": ["option1", "option2", "option3"]
					},
					"titledSingle": {
						"type": "string",
						"oneOf": [
							{"const": "value1", "title": "First Option"},
							{"const": "value2", "title": "Second Option"},
							{"const": "value3", "title": "Third Option"}
						]
					},
					"legacyEnum": {
						"type": "string",
						"enum": ["opt1", "opt2", "opt3"],
						"enumNames": ["Option One", "Option Two", "Option Three"]
					},
					"untitledMulti": {
						"type": "array",
						"items": {
							"type": "string",
							"enum": ["option1", "option2", "option3"]
						}
					},
					"titledMulti": {
						"type": "array",
						"items": {
							"anyOf": [
								{"const": "value1", "title": "First Choice"},
								{"const": "value2", "title": "Second Choice"},
								{"const": "value3", "title": "Third Choice"}
							]
						}
					}
				}
			}`)
			result, err := core.Elicit(ctx, core.ElicitationRequest{
				Message:         "Please make your selections",
				RequestedSchema: schema,
			})
			if err != nil {
				return core.ErrorResult(fmt.Sprintf("elicitation failed: %v", err)), nil
			}
			contentJSON, _ := json.Marshal(result.Content)
			return core.TextResult(fmt.Sprintf("Elicitation completed: action=%s, content=%s", result.Action, string(contentJSON))), nil
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
