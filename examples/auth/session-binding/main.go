// Example: Session hijacking prevention.
//
// Two users can connect — but one user cannot use another's session.
// The server binds Claims.Subject to the session at creation time.
//
// Run: go run ./session-binding -addr :8084
// The server prints tokens for alice and bob.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/auth/common"
	mcpcommon "github.com/panyam/mcpkit/examples/common"
	"github.com/panyam/mcpkit/ext/auth"
	"github.com/panyam/mcpkit/server"
)

func main() {
	addr := flag.String("addr", ":8084", "listen address")
	flag.Parse()

	env := common.NewEnv([]string{"read"})
	defer env.Close()

	listenURL := fmt.Sprintf("http://localhost%s", *addr)
	validator := env.NewValidator(listenURL)

	opts := mcpcommon.MCPServerOptions(*addr, "[mcp] ")
	opts = append(opts, server.WithAuth(validator))
	srv := server.NewServer(
		core.ServerInfo{Name: "auth-session-binding", Version: "1.0"},
		opts...,
	)
	common.RegisterEchoTools(srv)

	tokAlice := env.MintToken("alice", []string{"read"})
	tokBob := env.MintToken("bob", []string{"read"})

	log.Printf("Session hijacking prevention example on %s", *addr)
	log.Printf("Connect MCPJam: http://localhost%s/mcp", *addr)
	log.Printf("")
	log.Printf("Tokens (each creates a session bound to the user's sub claim):")
	log.Printf("  alice: %s", tokAlice)
	log.Printf("  bob:   %s", tokBob)
	log.Printf("")
	log.Printf("Try: connect with alice's token, note session ID.")
	log.Printf("Then try sending a request with bob's token to alice's session → 403")

	if err := srv.Run(*addr,
		server.WithStreamableHTTP(true),
		server.WithMux(func(mux *http.ServeMux) {
			auth.MountAuth(mux, auth.AuthConfig{
				ResourceURI:          listenURL,
				AuthorizationServers: []string{env.AS.Issuer()},
				ScopesSupported:      env.Scopes,
				MCPPath:              "/mcp",
			})
		}),
	); err != nil {
		log.Fatal(err)
	}
}
