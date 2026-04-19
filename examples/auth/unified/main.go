// Unified auth example — one MCP server demonstrating all auth patterns layered together:
//
//   - JWT/JWKS validation (real RS256 via in-process authorization server)
//   - Public discovery (tools/list works without a token)
//   - Scope enforcement (tools require different scopes)
//   - Session binding (switching tokens mid-session is rejected)
//
// Run: go run ./unified
// The server prints tokens for each exercise scenario.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/auth/common"
	"github.com/panyam/mcpkit/ext/auth"
	"github.com/panyam/mcpkit/server"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	env := common.NewEnv([]string{"read", "write", "admin"})
	defer env.Close()

	listenURL := fmt.Sprintf("http://localhost%s", *addr)
	validator := env.NewValidator(listenURL)

	srv := server.NewServer(
		core.ServerInfo{Name: "auth-unified", Version: "1.0"},
		server.WithAuth(validator),
		server.WithPublicMethods("initialize", "notifications/initialized", "tools/list", "prompts/list", "ping"),
		server.WithMiddleware(server.LoggingMiddleware(log.Default())),
	)
	common.RegisterEchoTools(srv)

	mux := http.NewServeMux()
	mux.Handle("/mcp", srv.Handler(server.WithStreamableHTTP(true)))
	auth.MountAuth(mux, auth.AuthConfig{
		ResourceURI:          listenURL,
		AuthorizationServers: []string{env.AS.Issuer()},
		ScopesSupported:      env.Scopes,
		MCPPath:              "/mcp",
	})

	// Mint tokens for each exercise scenario.
	tokReadOnly := env.MintToken("alice", []string{"read"})
	tokReadWrite := env.MintToken("alice", []string{"read", "write"})
	tokAll := env.MintToken("alice", []string{"read", "write", "admin"})
	tokBob := env.MintToken("bob", []string{"read", "write", "admin"})

	log.Printf("Unified auth example on %s", *addr)
	log.Printf("MCP endpoint: http://localhost%s/mcp", *addr)
	log.Printf("")
	log.Printf("=== Exercise 1: Public Discovery ===")
	log.Printf("Connect WITHOUT a token. tools/list works. Call echo → 401.")
	log.Printf("")
	log.Printf("=== Exercise 2: JWT Authentication ===")
	log.Printf("Connect with alice's read-only token. Call echo → see identity.")
	log.Printf("  Token (read):            %s", tokReadOnly)
	log.Printf("")
	log.Printf("=== Exercise 3: Scope Enforcement ===")
	log.Printf("With read-only token: echo works, write-tool fails, admin-tool fails.")
	log.Printf("Reconnect with broader tokens to unlock more tools.")
	log.Printf("  Token (read+write):      %s", tokReadWrite)
	log.Printf("  Token (read+write+admin): %s", tokAll)
	log.Printf("")
	log.Printf("=== Exercise 4: Session Binding ===")
	log.Printf("Connect as alice. Then try bob's token on alice's session → 403.")
	log.Printf("  Token (bob, all scopes): %s", tokBob)

	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}
