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

	"github.com/panyam/mcpkit/examples/auth/common"
	mcpcommon "github.com/panyam/mcpkit/examples/common"
	"github.com/panyam/mcpkit/server"
)

func main() {
	addr := flag.String("addr", ":8081", "listen address")
	wire := mcpcommon.RegisterWireFlags(flag.CommandLine)
	flag.Parse()

	log.Printf("Token: my-secret-token")
	log.Printf("Connect MCPJam: http://localhost%s/mcp with header Authorization: Bearer my-secret-token", *addr)

	if err := mcpcommon.RunServer(mcpcommon.ServerConfig{
		Name:    "auth-bearer",
		Version: "1.0",
		Addr:    *addr,
		Wire:    wire,
		Options: []server.Option{
			server.WithBearerToken("my-secret-token"),
		},
		Register: func(srv *server.Server) {
			common.RegisterEchoTools(srv)
		},
	}); err != nil {
		log.Fatal(err)
	}
}
