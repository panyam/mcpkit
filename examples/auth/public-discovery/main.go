// Example: Pre-auth capability discovery.
//
// Server with JWT auth + WithPublicMethods — clients can discover tools
// before authenticating. tools/call still requires a valid token.
//
// Run: go run ./public-discovery -addr :8085
// Try connecting MCPJam WITHOUT a token — tools/list works!
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
	addr := flag.String("addr", ":8085", "listen address")
	flag.Parse()

	env := common.NewEnv([]string{"read"})
	defer env.Close()

	listenURL := fmt.Sprintf("http://localhost%s", *addr)
	validator := env.NewValidator(listenURL)

	srv := server.NewServer(
		core.ServerInfo{Name: "auth-public-discovery", Version: "1.0"},
		server.WithAuth(validator),
		server.WithPublicMethods("initialize", "notifications/initialized", "tools/list", "prompts/list", "ping"),
		server.WithMiddleware(server.LoggingMiddleware(log.Default())),
	)
	common.RegisterEchoTools(srv)

	token := env.MintToken("alice", []string{"read"})

	log.Printf("Pre-auth discovery example on %s", *addr)
	log.Printf("Connect MCPJam: http://localhost%s/mcp", *addr)
	log.Printf("")
	log.Printf("Public methods: initialize, tools/list, prompts/list, ping")
	log.Printf("Protected: tools/call (needs token)")
	log.Printf("")
	log.Printf("1. Connect WITHOUT token — tools/list works, tools/call returns 401")
	log.Printf("2. Connect WITH token — everything works")
	log.Printf("")
	log.Printf("Token: %s", token)

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
