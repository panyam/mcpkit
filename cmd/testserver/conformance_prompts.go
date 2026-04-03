package main

// Conformance prompts implement the prompt contracts expected by the official
// MCP conformance test suite (@modelcontextprotocol/conformance).

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/panyam/mcpkit"
)

// registerConformancePrompts adds all prompts required by the MCP conformance suite.
func registerConformancePrompts(srv *mcpkit.Server) {
	// test_simple_prompt — no arguments, returns a simple text message
	srv.RegisterPrompt(
		mcpkit.PromptDef{
			Name:        "test_simple_prompt",
			Description: "A simple prompt for conformance testing",
		},
		func(ctx context.Context, req mcpkit.PromptRequest) (mcpkit.PromptResult, error) {
			return mcpkit.PromptResult{
				Description: "A simple test prompt",
				Messages: []mcpkit.PromptMessage{{
					Role:    "user",
					Content: mcpkit.Content{Type: "text", Text: "This is a simple prompt for testing."},
				}},
			}, nil
		},
	)

	// test_prompt_with_arguments — takes arg1 and arg2 arguments
	srv.RegisterPrompt(
		mcpkit.PromptDef{
			Name:        "test_prompt_with_arguments",
			Description: "A prompt with arguments for conformance testing",
			Arguments: []mcpkit.PromptArgument{
				{Name: "arg1", Description: "First test argument", Required: true},
				{Name: "arg2", Description: "Second test argument", Required: true},
			},
		},
		func(ctx context.Context, req mcpkit.PromptRequest) (mcpkit.PromptResult, error) {
			arg1 := req.Arguments["arg1"]
			arg2 := req.Arguments["arg2"]
			return mcpkit.PromptResult{
				Description: "A prompt with arguments",
				Messages: []mcpkit.PromptMessage{{
					Role:    "user",
					Content: mcpkit.Content{Type: "text", Text: fmt.Sprintf("Prompt with arguments: arg1='%s', arg2='%s'", arg1, arg2)},
				}},
			}, nil
		},
	)

	// test_prompt_with_embedded_resource — returns message with embedded resource content
	srv.RegisterPrompt(
		mcpkit.PromptDef{
			Name:        "test_prompt_with_embedded_resource",
			Description: "A prompt that includes an embedded resource",
			Arguments: []mcpkit.PromptArgument{
				{Name: "resourceUri", Description: "URI of the resource to embed", Required: true},
			},
		},
		func(ctx context.Context, req mcpkit.PromptRequest) (mcpkit.PromptResult, error) {
			uri := req.Arguments["resourceUri"]
			return mcpkit.PromptResult{
				Description: "A prompt with an embedded resource",
				Messages: []mcpkit.PromptMessage{{
					Role: "user",
					Content: mcpkit.Content{
						Type: "resource",
						Resource: &mcpkit.ResourceContent{
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
		mcpkit.PromptDef{
			Name:        "test_prompt_with_image",
			Description: "A prompt that includes an image",
		},
		func(ctx context.Context, req mcpkit.PromptRequest) (mcpkit.PromptResult, error) {
			pngBytes := minimalPNG() // reuse from conformance_tools.go
			return mcpkit.PromptResult{
				Description: "A prompt with image content",
				Messages: []mcpkit.PromptMessage{{
					Role: "user",
					Content: mcpkit.Content{
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
		func(ctx context.Context, ref mcpkit.CompletionRef, arg mcpkit.CompletionArgument) (mcpkit.CompletionResult, error) {
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
			return mcpkit.CompletionResult{
				Values:  filtered,
				Total:   len(filtered),
				HasMore: false,
			}, nil
		},
	)
}
