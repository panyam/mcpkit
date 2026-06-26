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

	"github.com/panyam/mcpkit/examples/auth/common"
	mcpcommon "github.com/panyam/mcpkit/examples/common"
	"github.com/panyam/mcpkit/ext/auth"
	"github.com/panyam/mcpkit/server"
)

func main() {
	addr := flag.String("addr", ":8082", "listen address")
	wire := mcpcommon.RegisterWireFlags(flag.CommandLine)
	flag.Parse()

	env := common.NewEnv([]string{"read", "write"})
	defer env.Close()

	// Create validator — audience is the server's own URL.
	listenURL := fmt.Sprintf("http://localhost%s", *addr)
	validator := env.NewValidator(listenURL)

	// Print a token for the user to copy-paste.
	token := env.MintToken("alice", []string{"read", "write"})
	log.Printf("AS: %s (JWKS: %s)", env.AS.URL(), env.AS.JWKSURL())
	log.Printf("")
	log.Printf("Connect MCPJam: http://localhost%s/mcp", *addr)
	log.Printf("Authorization: Bearer %s", token)

	if err := mcpcommon.RunServer(mcpcommon.ServerConfig{
		Name:    "auth-jwt",
		Version: "1.0",
		Addr:    *addr,
		Wire:    wire,
		Options: []server.Option{
			server.WithAuth(validator),
		},
		Register: func(srv *server.Server) {
			common.RegisterEchoTools(srv)
		},
		TransportOptions: []server.TransportOption{
			server.WithMux(func(mux *http.ServeMux) {
				auth.MountAuth(mux, auth.AuthConfig{
					ResourceURI:          listenURL,
					AuthorizationServers: []string{env.AS.Issuer()},
					ScopesSupported:      env.Scopes,
					MCPPath:              "/mcp",
				})
			}),
		},
	}); err != nil {
		log.Fatal(err)
	}
}
