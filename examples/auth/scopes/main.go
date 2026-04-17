// Example: Scope enforcement with step-up authorization.
//
// Server has tools requiring different scopes. Try calling write-tool
// with a read-only token — it fails. Get a broader token — it works.
//
// Run: go run ./scopes -addr :8083
// The server prints tokens with different scope sets.
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
	addr := flag.String("addr", ":8083", "listen address")
	flag.Parse()

	env := common.NewEnv([]string{"read", "write", "admin"})
	defer env.Close()

	listenURL := fmt.Sprintf("http://localhost%s", *addr)
	validator := env.NewValidator(listenURL)

	srv := server.NewServer(
		core.ServerInfo{Name: "auth-scopes", Version: "1.0"},
		server.WithAuth(validator),
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

	tokRead := env.MintToken("alice", []string{"read"})
	tokReadWrite := env.MintToken("alice", []string{"read", "write"})
	tokAll := env.MintToken("alice", []string{"read", "write", "admin"})

	log.Printf("Scope enforcement example on %s", *addr)
	log.Printf("Connect MCPJam: http://localhost%s/mcp", *addr)
	log.Printf("")
	log.Printf("Tokens (copy-paste into Authorization: Bearer <token>):")
	log.Printf("  read only:        %s", tokRead)
	log.Printf("  read+write:       %s", tokReadWrite)
	log.Printf("  read+write+admin: %s", tokAll)
	log.Printf("")
	log.Printf("Try: echo (any token), write-tool (needs write), admin-tool (needs admin)")

	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}
