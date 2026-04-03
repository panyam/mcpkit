// testserver is a minimal MCP server for manual testing and conformance validation.
// It registers three tools (echo, add, fail) and serves HTTP+SSE on :8787.
//
// Usage:
//
//	go run ./cmd/testserver
//	curl -N http://localhost:8787/mcp/sse
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/panyam/mcpkit"
)

func main() {
	srv := mcpkit.NewServer(
		mcpkit.ServerInfo{
			Name:    "mcpkit-testserver",
			Version: "0.1.0",
		},
		mcpkit.WithListen(":8787"),
		mcpkit.WithToolTimeout(30*time.Second),
	)

	// echo: returns the input message as-is
	srv.RegisterTool(
		mcpkit.ToolDef{
			Name:        "echo",
			Description: "Echoes the input message",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"message": map[string]any{"type": "string", "description": "The message to echo"},
				},
				"required": []string{"message"},
			},
		},
		func(ctx context.Context, req mcpkit.ToolRequest) (mcpkit.ToolResult, error) {
			var args struct {
				Message string `json:"message"`
			}
			if err := req.Bind(&args); err != nil {
				return mcpkit.ErrorResult(err.Error()), nil
			}
			return mcpkit.TextResult("echo: " + args.Message), nil
		},
	)

	// add: adds two numbers
	srv.RegisterTool(
		mcpkit.ToolDef{
			Name:        "add",
			Description: "Adds two numbers",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"a": map[string]any{"type": "number", "description": "First number"},
					"b": map[string]any{"type": "number", "description": "Second number"},
				},
				"required": []string{"a", "b"},
			},
		},
		func(ctx context.Context, req mcpkit.ToolRequest) (mcpkit.ToolResult, error) {
			var args struct {
				A json.Number `json:"a"`
				B json.Number `json:"b"`
			}
			if err := req.Bind(&args); err != nil {
				return mcpkit.ErrorResult(err.Error()), nil
			}
			a, _ := args.A.Float64()
			b, _ := args.B.Float64()
			return mcpkit.TextResult(fmt.Sprintf("%g", a+b)), nil
		},
	)

	// fail: always returns an error (for testing isError semantics)
	srv.RegisterTool(
		mcpkit.ToolDef{
			Name:        "fail",
			Description: "Always fails with an error",
			InputSchema: map[string]any{
				"type": "object",
			},
		},
		func(ctx context.Context, req mcpkit.ToolRequest) (mcpkit.ToolResult, error) {
			return mcpkit.ToolResult{}, fmt.Errorf("intentional failure for testing")
		},
	)

	log.Println("MCP test server listening on :8787 (SSE at /mcp/sse)")
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
