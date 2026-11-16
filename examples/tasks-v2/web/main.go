// Command web serves the tasks-v2 example's tools with nothing else around them:
// no walkthrough, no telemetry. Built with GOOS=js GOARCH=wasm, it runs in a
// Web Worker under the shared playground page (examples/common/web). Natively,
// `go run ./web` serves the same tools over Streamable HTTP on :8080.
package main

import (
	"log"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/tasks-v2/mcp"
	"github.com/panyam/mcpkit/server"
)

func main() {
	srv := server.NewServer(core.ServerInfo{Name: "tasks-v2-demo", Version: "0.1.0"})
	mcp.Register(srv)
	log.Fatal(srv.Run(":8080"))
}
