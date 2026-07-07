// Command client is the minimal mcpkit MCP client used by the Get Started
// guide: connect, call one tool, print the result. Run the server first
// (see ../server), then `go run .` here.
package main

import (
	"fmt"
	"log"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
)

func main() {
	c := client.NewClient("http://localhost:8787/mcp",
		core.ClientInfo{Name: "getting-started-client", Version: "0.1.0"},
	)
	if err := c.Connect(); err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer c.Close()

	// ToolCall sends tools/call and returns the tool's text output directly.
	out, err := c.ToolCall("greet", map[string]any{"name": "world"})
	if err != nil {
		log.Fatalf("greet: %v", err)
	}
	fmt.Println(out) // Hello, world!
}
