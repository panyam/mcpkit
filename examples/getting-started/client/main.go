// Command client is the minimal mcpkit MCP client used by the Get Started
// guide: connect, call one tool, print the result. Run the server first
// (see ../server), then `go run .` here.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
)

func main() {
	// Every client method that talks to the server takes a context, so
	// cancellation and timeouts work the same way everywhere.
	ctx := context.Background()

	c := client.NewClient("http://localhost:8787/mcp",
		core.ClientInfo{Name: "getting-started-client", Version: "0.1.0"},
	)
	// Connect's context bounds the handshake only. The session it opens
	// outlives that context; use Close to end it.
	if err := c.Connect(ctx); err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer c.Close()

	// ToolCall sends tools/call and returns the tool's text output directly.
	out, err := c.ToolCall(ctx, "greet", map[string]any{"name": "world"})
	if err != nil {
		log.Fatalf("greet: %v", err)
	}
	fmt.Println(out) // Hello, world!
}
