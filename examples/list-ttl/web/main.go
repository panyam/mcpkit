// Command web serves the list-ttl example's tools with nothing else around them:
// no walkthrough, no telemetry. Built with GOOS=js GOARCH=wasm, it runs in a
// Web Worker under the shared playground page (examples/common/web). Natively,
// `go run ./web` serves the same tools over Streamable HTTP on :8080.
package main

import (
	"log"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/list-ttl/mcp"
	"github.com/panyam/mcpkit/server"
)

func main() {
	// The same 60-second TTL `make serve` uses, so every list carries a hint.
	srv := server.NewServer(core.ServerInfo{Name: "list-ttl-demo", Version: "0.1.0"}, mcp.Options(60000, "")...)
	mcp.Register(srv)
	log.Fatal(srv.Run(":8080"))
}
