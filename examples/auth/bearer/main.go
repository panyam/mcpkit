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
	mcpcommon "github.com/panyam/mcpkit/examples/common"
	"github.com/panyam/mcpkit/server"
)

func main() {
	addr := flag.String("addr", ":8081", "listen address")
	flag.Parse()

	opts := mcpcommon.MCPServerOptions(*addr, "[mcp] ")
	opts = append(opts, server.WithBearerToken("my-secret-token"))
	srv := server.NewServer(
		core.ServerInfo{Name: "auth-bearer", Version: "1.0"},
		opts...,
	)
	common.RegisterEchoTools(srv)

	log.Printf("Bearer auth example on %s (token: my-secret-token)", *addr)
	log.Printf("Connect MCPJam: http://localhost%s/mcp with header Authorization: Bearer my-secret-token", *addr)
	if err := srv.Run(*addr); err != nil {
		log.Fatal(err)
	}
}
