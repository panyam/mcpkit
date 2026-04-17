// Example: Scope enforcement with step-up authorization.
//
// Server has tools requiring different scopes. Client starts with read-only,
// gets rejected on write, then upgrades scopes and succeeds.
//
// Run: go run ./scopes
package main

import (
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
	fmt.Println("=== Auth Example: Scope Enforcement + Step-Up ===")
	fmt.Println()

	// Step 1: Start in-process authorization server.
	as, err := testutil.NewAuthServer(testutil.WithScopes([]string{"read", "write", "admin"}))
	if err != nil {
		log.Fatal(err)
	}
	defer as.Close()

	// Step 2: Create MCP server with JWT auth + scoped tools.
	var handler http.Handler
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
	}))
	defer ts.Close()

	validator := auth.NewJWTValidator(auth.JWTConfig{
		JWKSURL:   as.JWKSURL(),
		Issuer:    as.Issuer(),
		Audience: "", // disabled for simplicity — production should validate
		AllScopes: []string{"read", "write", "admin"},
	})
	validator.Start()
	defer validator.Stop()

	srv := server.NewServer(
		core.ServerInfo{Name: "scopes-demo", Version: "1.0"},
		server.WithAuth(validator),
	)

	// echo: no scope required
	srv.Register(core.TextTool[struct{}]("echo", "No scope required — anyone can call",
		func(ctx core.ToolContext, _ struct{}) (string, error) {
			return "echo ok", nil
		},
	))

	// write-tool: requires "write" scope
	srv.Register(core.TextTool[struct{}]("write-tool", "Requires write scope",
		func(ctx core.ToolContext, _ struct{}) (string, error) {
			if err := auth.RequireScope(ctx, "write"); err != nil {
				return "error: " + err.Error(), nil
			}
			return "write ok", nil
		},
	))

	// admin-tool: requires "admin" scope
	srv.Register(core.TextTool[struct{}]("admin-tool", "Requires admin scope",
		func(ctx core.ToolContext, _ struct{}) (string, error) {
			if err := auth.RequireScope(ctx, "admin"); err != nil {
				return "error: " + err.Error(), nil
			}
			return "admin ok", nil
		},
	))

	handler = srv.Handler(server.WithStreamableHTTP(true))
	fmt.Printf("Server at %s (scopes: read, write, admin)\n\n", ts.URL)

	// Step 3: Connect with read-only scope.
	fmt.Println("Step 1: Connect with scope=read...")
	tokRead, _ := as.MintTokenForSubject("alice", []string{"read"})
	c1 := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "demo", Version: "1.0"},
		client.WithClientBearerToken(tokRead),
	)
	if err := c1.Connect(); err != nil {
		fmt.Printf("  → Connect failed: %v\n", err)
		return
	}

	r1, _ := c1.ToolCall("echo", nil)
	fmt.Printf("  → echo: %s ✓\n", r1)

	r2, _ := c1.ToolCall("write-tool", nil)
	fmt.Printf("  → write-tool: %s (rejected — no write scope) ✓\n", r2)
	c1.Close()

	// Step 4: Upgrade to read+write scope.
	fmt.Println("\nStep 2: Upgrade to scope=read+write...")
	tokWrite, _ := as.MintTokenForSubject("alice", []string{"read", "write"})
	c2 := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "demo", Version: "1.0"},
		client.WithClientBearerToken(tokWrite),
	)
	if err := c2.Connect(); err != nil {
		fmt.Printf("  → Connect failed: %v\n", err)
		return
	}

	r3, _ := c2.ToolCall("write-tool", nil)
	fmt.Printf("  → write-tool: %s ✓\n", r3)

	r4, _ := c2.ToolCall("admin-tool", nil)
	fmt.Printf("  → admin-tool: %s (rejected — no admin scope) ✓\n", r4)
	c2.Close()

	fmt.Println("\n=== Done: Scope enforcement works! ===")
}
