// Command web serves the skills-core example's tools with nothing else around them:
// no walkthrough, no telemetry. Built with GOOS=js GOARCH=wasm, it runs in a
// Web Worker under the shared playground page (examples/common/web). Natively,
// `go run ./web` serves the same tools over Streamable HTTP on :8080.
package main

import (
	"log"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/skills-core/mcp"
	"github.com/panyam/mcpkit/server"
)

func main() {
	srv := server.NewServer(core.ServerInfo{Name: "skills-core", Version: "0.1.0"})
	if err := mcp.Register(srv, ""); err != nil {
		log.Fatal(err)
	}
	log.Fatal(srv.Run(":8080"))
}
