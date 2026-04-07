// testserver is a minimal MCP server for manual testing and conformance validation.
// It registers three tools (echo, add, fail) and serves MCP transports on :8787.
//
// By default, serves SSE transport. Set STREAMABLE=1 for Streamable HTTP,
// or BOTH=1 for both transports simultaneously.
//
// Usage:
//
//	go run ./cmd/testserver
//	STREAMABLE=1 go run ./cmd/testserver
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/panyam/mcpkit"
)

func listenAddr() string {
	if port := os.Getenv("PORT"); port != "" {
		return ":" + port
	}
	return ":8787"
}

func main() {
	var serverOpts []mcpkit.Option
	serverOpts = append(serverOpts,
		mcpkit.WithListen(listenAddr()),
		mcpkit.WithToolTimeout(30*time.Second),
	)
	// Enable HTTP-level request logging if VERBOSE is set
	if os.Getenv("VERBOSE") == "1" {
		serverOpts = append(serverOpts, mcpkit.WithRequestLogging(log.Default()))
	}
	srv := mcpkit.NewServer(
		mcpkit.ServerInfo{
			Name:    "mcpkit-testserver",
			Version: "0.1.0",
		},
		serverOpts...,
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

	// Register conformance suite tools, resources, and prompts
	registerConformanceTools(srv)
	registerConformanceResources(srv)
	registerConformancePrompts(srv)

	var transportOpts []mcpkit.TransportOption
	switch {
	case os.Getenv("BOTH") == "1":
		transportOpts = append(transportOpts, mcpkit.WithStreamableHTTP(true), mcpkit.WithSSE(true))
		log.Printf("MCP test server listening on %s (SSE + Streamable HTTP at /mcp)", listenAddr())
	case os.Getenv("STREAMABLE") == "1":
		transportOpts = append(transportOpts, mcpkit.WithStreamableHTTP(true), mcpkit.WithSSE(false))
		log.Printf("MCP test server listening on %s (Streamable HTTP at /mcp)", listenAddr())
	default:
		log.Printf("MCP test server listening on %s (SSE at /mcp/sse)", listenAddr())
	}
	if err := srv.ListenAndServe(transportOpts...); err != nil {
		log.Fatal(err)
	}
}
