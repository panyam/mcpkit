// Example: JWT/JWKS validation with claims propagation.
//
// Server validates RS256 JWTs via an in-process JWKS endpoint.
// The echo tool reports the authenticated user's identity.
//
// Run: go run ./jwt -addr :8082
// The server prints a token on startup — use it to connect MCPJam.
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
	addr := flag.String("addr", ":8082", "listen address")
	flag.Parse()

	env := common.NewEnv([]string{"read", "write"})
	defer env.Close()

	// Create validator — audience is the server's own URL.
	listenURL := fmt.Sprintf("http://localhost%s", *addr)
	validator := env.NewValidator(listenURL)

	srv := server.NewServer(
		core.ServerInfo{Name: "auth-jwt", Version: "1.0"},
		server.WithAuth(validator),
		server.WithMiddleware(server.LoggingMiddleware(log.Default())),
	)
	common.RegisterEchoTools(srv)

	// Mount PRM endpoints for auth discovery.
	mux := http.NewServeMux()
	mcpHandler := srv.Handler(server.WithStreamableHTTP(true))
	mux.Handle("/mcp", mcpHandler)
	auth.MountAuth(mux, auth.AuthConfig{
		ResourceURI:          listenURL,
		AuthorizationServers: []string{env.AS.Issuer()},
		ScopesSupported:      env.Scopes,
		MCPPath:              "/mcp",
	})

	// Print a token for the user to copy-paste.
	token := env.MintToken("alice", []string{"read", "write"})
	log.Printf("JWT auth example on %s", *addr)
	log.Printf("AS: %s (JWKS: %s)", env.AS.URL(), env.AS.JWKSURL())
	log.Printf("")
	log.Printf("Connect MCPJam: http://localhost%s/mcp", *addr)
	log.Printf("Authorization: Bearer %s", token)

	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}
