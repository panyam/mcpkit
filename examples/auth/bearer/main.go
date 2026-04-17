// Example: Static bearer token authentication.
//
// The simplest auth pattern — server validates a constant token.
// Connect MCPJam with: Authorization: Bearer my-secret-token
//
// Run: go run ./bearer -addr :8081
package main

import (
	"flag"
	"log"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/auth/common"
	"github.com/panyam/mcpkit/server"
)

func main() {
	addr := flag.String("addr", ":8081", "listen address")
	flag.Parse()

	srv := server.NewServer(
		core.ServerInfo{Name: "auth-bearer", Version: "1.0"},
		server.WithBearerToken("my-secret-token"),
		server.WithMiddleware(server.LoggingMiddleware(log.Default())),
	)
	common.RegisterEchoTools(srv)

	log.Printf("Bearer auth example on %s (token: my-secret-token)", *addr)
	log.Printf("Connect MCPJam: http://localhost%s/mcp with header Authorization: Bearer my-secret-token", *addr)
	if err := srv.Run(*addr); err != nil {
		log.Fatal(err)
	}
}
