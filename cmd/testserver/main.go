// testserver is a minimal MCP server for manual testing and conformance validation.
// It registers three tools (echo, add, fail) and serves MCP transports on :8787.
//
// By default, serves SSE transport. Set STREAMABLE=1 for Streamable HTTP,
// BOTH=1 for both transports simultaneously, or STDIO=1 for stdio transport
// (Content-Length framed JSON-RPC over stdin/stdout).
//
// Usage:
//
//	go run ./cmd/testserver
//	STREAMABLE=1 go run ./cmd/testserver
//	STDIO=1 go run ./cmd/testserver
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// Input types for typed tool registration.

type echoInput struct {
	Message string `json:"message" jsonschema:"description=The message to echo"`
}

type addInput struct {
	A float64 `json:"a" jsonschema:"description=First number"`
	B float64 `json:"b" jsonschema:"description=Second number"`
}

func listenAddr() string {
	if port := os.Getenv("PORT"); port != "" {
		return ":" + port
	}
	return ":8787"
}

func main() {
	var serverOpts []server.Option
	serverOpts = append(serverOpts,
		server.WithListen(listenAddr()),
		server.WithToolTimeout(30*time.Second),
		server.WithSubscriptions(),
		server.WithExtension(testUIExtension{}),
	)
	// Enable HTTP-level request logging if VERBOSE is set
	if os.Getenv("VERBOSE") == "1" {
		serverOpts = append(serverOpts, server.WithRequestLogging(log.Default()))
	}
	srv := server.NewServer(
		core.ServerInfo{
			Name:    "mcpkit-testserver",
			Version: "0.1.0",
		},
		serverOpts...,
	)

	// echo: returns the input message as-is
	srv.Register(core.TextTool[echoInput]("echo", "Echoes the input message",
		func(ctx core.ToolContext, input echoInput) (string, error) {
			return "echo: " + input.Message, nil
		},
	))

	// add: adds two numbers
	srv.Register(core.TextTool[addInput]("add", "Adds two numbers",
		func(ctx core.ToolContext, input addInput) (string, error) {
			return fmt.Sprintf("%g", input.A+input.B), nil
		},
	))

	// fail: always returns an error (for testing isError semantics)
	srv.Register(core.TextTool[struct{}]("fail", "Always fails with an error",
		func(ctx core.ToolContext, _ struct{}) (string, error) {
			return "", fmt.Errorf("intentional failure for testing")
		},
	))

	// Register conformance suite tools, resources, and prompts
	registerConformanceTools(srv)
	registerConformanceResources(srv)
	registerConformancePrompts(srv)
	registerConformanceApps(srv)

	// Self-test mode: creates an in-process client with LoggingTransport,
	// lists tools via auto-pagination iterator, and calls each one.
	// Demonstrates LoggingTransport (wire-level tracing) and iter.Seq2 iterators.
	//
	// Usage: SELFTEST=1 go run ./cmd/testserver
	if os.Getenv("SELFTEST") == "1" {
		selfTest(srv)
		return
	}

	// Stdio mode: Content-Length framed JSON-RPC over stdin/stdout.
	// No HTTP server — the process communicates directly via stdio.
	if os.Getenv("STDIO") == "1" {
		log.SetOutput(os.Stderr) // Keep debug output on stderr, not stdout
		log.Printf("MCP test server running on stdio")
		ctx := context.Background()
		if err := srv.RunStdio(ctx, server.WithStdioLogger(log.Default())); err != nil {
			log.Fatal(err)
		}
		return
	}

	var transportOpts []server.TransportOption
	switch {
	case os.Getenv("BOTH") == "1":
		transportOpts = append(transportOpts, server.WithStreamableHTTP(true), server.WithSSE(true))
		log.Printf("MCP test server listening on %s (SSE + Streamable HTTP at /mcp)", listenAddr())
	case os.Getenv("STREAMABLE") == "1":
		transportOpts = append(transportOpts, server.WithStreamableHTTP(true), server.WithSSE(false))
		log.Printf("MCP test server listening on %s (Streamable HTTP at /mcp)", listenAddr())
	default:
		log.Printf("MCP test server listening on %s (SSE at /mcp/sse)", listenAddr())
	}
	if err := srv.ListenAndServe(transportOpts...); err != nil {
		log.Fatal(err)
	}
}

// selfTest creates an in-process client with LoggingTransport enabled,
// lists all tools via the auto-pagination iterator, and calls each one.
// Demonstrates wire-level tracing and iter.Seq2 iterators.
func selfTest(srv *server.Server) {
	log.Println("=== Self-test mode: LoggingTransport + iterator demo ===")

	// Wrap the in-process transport with LoggingTransport for wire-level tracing.
	inner := server.NewInProcessTransport(srv)
	logged := &core.LoggingTransport{
		Inner:     inner,
		Logger:    log.New(os.Stderr, "", log.Ltime),
		LogBodies: os.Getenv("VERBOSE") == "1",
	}

	c := client.NewClient("selftest://", core.ClientInfo{Name: "selftest", Version: "1.0"},
		client.WithTransport(logged),
	)
	if err := c.Connect(); err != nil {
		log.Fatalf("self-test connect: %v", err)
	}
	defer c.Close()

	// List tools using auto-pagination iterator.
	log.Println("--- Listing tools via c.Tools(ctx) iterator ---")
	ctx := context.Background()
	count := 0
	for tool, err := range c.Tools(ctx) {
		if err != nil {
			log.Fatalf("tools iterator: %v", err)
		}
		log.Printf("  [%d] %s — %s", count+1, tool.Name, tool.Description)
		count++
	}
	log.Printf("--- %d tools found ---", count)

	// Call the echo tool.
	log.Println("--- Calling echo tool ---")
	result, err := c.ToolCall("echo", map[string]any{"message": "hello from self-test"})
	if err != nil {
		log.Printf("echo: error: %v", err)
	} else {
		log.Printf("echo: %s", result)
	}

	log.Println("=== Self-test complete ===")
}
