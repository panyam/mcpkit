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
	mcpcommon "github.com/panyam/mcpkit/examples/common"
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

	opts := mcpcommon.MCPServerOptions(*addr, "[mcp] ")
	opts = append(opts, server.WithAuth(validator))
	srv := server.NewServer(
		core.ServerInfo{Name: "auth-scopes", Version: "1.0"},
		opts...,
	)
	common.RegisterEchoTools(srv)
	srv.UseMiddleware(auth.NewToolScopeMiddleware(srv.Registry()))

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
