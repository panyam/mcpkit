// Example: Static bearer token authentication.
//
// The simplest auth pattern — server validates a constant token,
// client sends it in the Authorization header. No external dependencies.
//
// Run: go run ./bearer
package main

import (
	"fmt"
	"net/http/httptest"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

func main() {
	fmt.Println("=== Auth Example: Static Bearer Token ===")
	fmt.Println()

	// Step 1: Create server with bearer token auth.
	srv := server.NewServer(
		core.ServerInfo{Name: "bearer-demo", Version: "1.0"},
		server.WithAuth(core.BearerTokenValidator("my-secret-token")),
	)
	srv.Register(core.TextTool[struct{}]("ping", "Returns pong",
		func(ctx core.ToolContext, _ struct{}) (string, error) {
			return "pong", nil
		},
	))

	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	defer ts.Close()
	fmt.Printf("Server running at %s (auth: bearer token)\n\n", ts.URL)

	// Step 2: Try without token → 401.
	fmt.Println("Step 1: Connect WITHOUT token...")
	c1 := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "demo", Version: "1.0"})
	if err := c1.Connect(); err != nil {
		fmt.Printf("  → Rejected: %v ✓\n\n", err)
	}

	// Step 3: Try with wrong token → 401.
	fmt.Println("Step 2: Connect with WRONG token...")
	c2 := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "demo", Version: "1.0"},
		client.WithClientBearerToken("wrong-token"),
	)
	if err := c2.Connect(); err != nil {
		fmt.Printf("  → Rejected: %v ✓\n\n", err)
	}

	// Step 4: Try with correct token → 200.
	fmt.Println("Step 3: Connect with CORRECT token...")
	c3 := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "demo", Version: "1.0"},
		client.WithClientBearerToken("my-secret-token"),
	)
	if err := c3.Connect(); err != nil {
		fmt.Printf("  → Error: %v\n", err)
		return
	}
	defer c3.Close()
	result, err := c3.ToolCall("ping", nil)
	if err != nil {
		fmt.Printf("  → Tool call failed: %v\n", err)
		return
	}
	fmt.Printf("  → Connected and called ping: %s ✓\n", result)

	fmt.Println("\n=== Done ===")
}
