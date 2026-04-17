// Example: Pre-auth capability discovery.
//
// Server with JWT auth + WithPublicMethods allows unauthenticated clients
// to discover tools before deciding to authenticate.
//
// Run: go run ./public-discovery
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/auth"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/oneauth/testutil"
)

func main() {
	fmt.Println("=== Auth Example: Pre-Auth Capability Discovery ===")
	fmt.Println()

	// Step 1: Start AS + MCP server with public methods.
	as, err := testutil.NewAuthServer(testutil.WithScopes([]string{"read"}))
	if err != nil {
		log.Fatal(err)
	}
	defer as.Close()

	var handler http.Handler
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
	}))
	defer ts.Close()

	validator := auth.NewJWTValidator(auth.JWTConfig{
		JWKSURL:  as.JWKSURL(),
		Issuer:   as.Issuer(),
		Audience: "", // disabled for simplicity
	})
	validator.Start()
	defer validator.Stop()

	srv := server.NewServer(
		core.ServerInfo{Name: "discovery-demo", Version: "1.0"},
		server.WithAuth(validator),
		server.WithPublicMethods("initialize", "notifications/initialized", "tools/list", "prompts/list", "ping"),
	)

	srv.Register(core.TextTool[struct{}]("echo", "Echoes input",
		func(ctx core.ToolContext, _ struct{}) (string, error) { return "echo ok", nil },
	))
	srv.Register(core.TextTool[struct{}]("secret-tool", "Requires auth to call",
		func(ctx core.ToolContext, _ struct{}) (string, error) { return "secret!", nil },
	))

	handler = srv.Handler(server.WithStreamableHTTP(true))
	fmt.Printf("Server at %s\n", ts.URL)
	fmt.Println("Public methods: initialize, tools/list, ping")
	fmt.Println("Protected methods: tools/call (requires JWT)")

	// Step 2: Discover tools WITHOUT authentication.
	fmt.Println("\nStep 1: Discover tools without auth...")
	c1 := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "demo", Version: "1.0"})
	if err := c1.Connect(); err != nil {
		fmt.Printf("  → Connect failed: %v\n", err)
		return
	}

	count := 0
	for tool, err := range c1.Tools(context.Background()) {
		if err != nil {
			break
		}
		fmt.Printf("  → Found tool: %s — %s\n", tool.Name, tool.Description)
		count++
	}
	fmt.Printf("  → Discovered %d tools without auth ✓\n", count)

	// Step 3: Try to call a tool without auth → fails.
	fmt.Println("\nStep 2: Try tools/call without auth...")
	_, err = c1.ToolCall("echo", nil)
	if err != nil {
		fmt.Printf("  → Rejected: %v ✓\n", err)
	}
	c1.Close()

	// Step 4: Authenticate and call.
	fmt.Println("\nStep 3: Authenticate and call tools/call...")
	tok, _ := as.MintTokenForSubject("alice", []string{"read"})
	c2 := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "demo", Version: "1.0"},
		client.WithClientBearerToken(tok),
	)
	if err := c2.Connect(); err != nil {
		fmt.Printf("  → Connect failed: %v\n", err)
		return
	}
	defer c2.Close()

	result, err := c2.ToolCall("echo", nil)
	if err != nil {
		fmt.Printf("  → Error: %v\n", err)
		return
	}
	fmt.Printf("  → echo: %s ✓\n", result)

	fmt.Println("\n=== Done: Discovered tools before auth, called after auth! ===")
}
