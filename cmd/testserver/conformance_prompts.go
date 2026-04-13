package main

// Conformance prompts implement the prompt contracts expected by the official
// MCP conformance test suite (@modelcontextprotocol/conformance).

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// registerConformancePrompts adds all prompts required by the MCP conformance suite.
func registerConformancePrompts(srv *server.Server) {
	// test_simple_prompt — no arguments, returns a simple text message
	srv.RegisterPrompt(
		core.PromptDef{
			Name:        "test_simple_prompt",
			Description: "A simple prompt for conformance testing",
		},
		func(ctx core.PromptContext, req core.PromptRequest) (core.PromptResult, error) {
			return core.PromptResult{
				Description: "A simple test prompt",
				Messages: []core.PromptMessage{{
					Role:    "user",
					Content: core.Content{Type: "text", Text: "This is a simple prompt for testing."},
				}},
			}, nil
		},
	)

	// test_prompt_with_arguments — takes arg1 and arg2 arguments
	srv.RegisterPrompt(
		core.PromptDef{
			Name:        "test_prompt_with_arguments",
			Description: "A prompt with arguments for conformance testing",
			Arguments: []core.PromptArgument{
				{Name: "arg1", Description: "First test argument", Required: true},
				{Name: "arg2", Description: "Second test argument", Required: true},
			},
		},
		func(ctx core.PromptContext, req core.PromptRequest) (core.PromptResult, error) {
			arg1 := req.Arguments["arg1"]
			arg2 := req.Arguments["arg2"]
			return core.PromptResult{
				Description: "A prompt with arguments",
				Messages: []core.PromptMessage{{
					Role:    "user",
					Content: core.Content{Type: "text", Text: fmt.Sprintf("Prompt with arguments: arg1='%s', arg2='%s'", arg1, arg2)},
				}},
			}, nil
		},
	)

	// test_prompt_with_embedded_resource — returns message with embedded resource content
	srv.RegisterPrompt(
		core.PromptDef{
			Name:        "test_prompt_with_embedded_resource",
			Description: "A prompt that includes an embedded resource",
			Arguments: []core.PromptArgument{
				{Name: "resourceUri", Description: "URI of the resource to embed", Required: true},
			},
		},
		func(ctx core.PromptContext, req core.PromptRequest) (core.PromptResult, error) {
			uri, _ := req.Arguments["resourceUri"].(string)
			return core.PromptResult{
				Description: "A prompt with an embedded resource",
				Messages: []core.PromptMessage{{
					Role: "user",
					Content: core.Content{
						Type: "resource",
						Resource: &core.ResourceContent{
							URI:      uri,
							MimeType: "text/plain",
							Text:     "Embedded resource content for testing.",
						},
					},
				}},
			}, nil
		},
	)

	// test_prompt_with_image — returns message with image content
	srv.RegisterPrompt(
		core.PromptDef{
			Name:        "test_prompt_with_image",
			Description: "A prompt that includes an image",
		},
		func(ctx core.PromptContext, req core.PromptRequest) (core.PromptResult, error) {
			pngBytes := minimalPNG() // reuse from conformance_tools.go
			return core.PromptResult{
				Description: "A prompt with image content",
				Messages: []core.PromptMessage{{
					Role: "user",
					Content: core.Content{
						Type:     "image",
						MimeType: "image/png",
						Data:     base64.StdEncoding.EncodeToString(pngBytes),
					},
				}},
			}, nil
		},
	)

	// Register completion handler for test_prompt_with_arguments.
	// The conformance suite sends completion/complete with ref/prompt for this prompt
	// and expects a valid response with completion suggestions.
	srv.RegisterCompletion("ref/prompt", "test_prompt_with_arguments",
		func(ctx core.PromptContext, ref core.CompletionRef, arg core.CompletionArgument) (core.CompletionResult, error) {
			// Provide sample completions filtered by partial input
			allValues := []string{"value1", "value2", "value3"}
			var filtered []string
			for _, v := range allValues {
				if strings.HasPrefix(v, arg.Value) {
					filtered = append(filtered, v)
				}
			}
			if filtered == nil {
				filtered = allValues
			}
			return core.CompletionResult{
				Values:  filtered,
				Total:   len(filtered),
				HasMore: false,
			}, nil
		},
	)
}
