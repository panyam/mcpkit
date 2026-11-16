// Command web serves the file-inputs example's tools with nothing else around
// them: no walkthrough, no telemetry, no upload directory. Built with GOOS=js
// GOARCH=wasm, it runs in a Web Worker under the shared playground page
// (examples/common/web). Natively, `go run ./web` serves the same tools over
// Streamable HTTP on :8080.
package main

import (
	"log"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/file-inputs/mcp"
	"github.com/panyam/mcpkit/ext/ui"
	"github.com/panyam/mcpkit/server"
)

func main() {
	srv := server.NewServer(core.ServerInfo{Name: "file-inputs-demo", Version: "0.1.0"},
		server.WithExtension(&ui.UIExtension{}),
		server.WithFileInputValidation(),
	)
	mcp.Register(srv, mcp.Options{})
	log.Fatal(srv.Run(":8080"))
}
