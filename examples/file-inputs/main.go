// Example: SEP-2356 File Inputs — declarative file pickers for tools.
//
// Two-process architecture:
//
//	Terminal 1:  just serve         # MCP server on :8080
//	Terminal 2:  just demo          # demokit walkthrough (or `just demo --tui`)
//
// The server is a real MCP server — any host (VS Code, Claude Desktop,
// MCPJam) can connect to it and observe the SEP-2356 `x-mcp-file` keyword
// on tool inputSchemas. The walkthrough acts as a scripted MCP host that
// reads real files from disk, encodes them as RFC 2397 data URIs, and
// invokes each tool via tools/call.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"strings"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/examples/common"
	commonotel "github.com/panyam/mcpkit/examples/common/otel"
	"github.com/panyam/mcpkit/examples/file-inputs/mcp"
	"github.com/panyam/mcpkit/ext/ui"
	"github.com/panyam/mcpkit/server"
)

func main() {
	for _, arg := range os.Args[1:] {
		switch strings.TrimSpace(arg) {
		case "--serve":
			serve()
			return
		}
	}
	runDemo()
}

func serve() {
	addr := flag.String("addr", ":8080", "listen address")
	tel := common.RegisterTelemetryFlags(flag.CommandLine)
	wire := common.RegisterWireFlags(flag.CommandLine)
	flag.CommandLine.Parse(demokit.FilterArgs(os.Args[1:],
		demokit.BoolFlag("--serve"),
		demokit.ValueFlag("--url"),
		demokit.ValueFlag("--file"),
	))

	tp, shutdown, err := commonotel.SetupTelemetry(context.Background(),
		commonotel.WithExporter(*tel.Exporter),
		commonotel.WithOTLPEndpoint(*tel.OTLPEndpoint),
		commonotel.WithServiceName("file-inputs-demo"),
	)
	if err != nil {
		log.Fatalf("commonotel.SetupTelemetry: %v", err)
	}
	defer shutdown(context.Background())

	// One temp dir per server run, NOT auto-cleaned — the whole point is
	// that you can `ls` the directory and `open` the files after the
	// walkthrough finishes. A real handler would never persist user
	// uploads under a predictable path; this is purely for inspection.
	uploadDir, err := os.MkdirTemp("/tmp", "file-inputs-demo-")
	if err != nil {
		log.Fatalf("mktemp: %v", err)
	}

	log.Printf("[file-inputs-demo] POST /mcp")
	log.Printf("[file-inputs-demo] tools: upload_image, analyze_documents, process_any_file")
	log.Printf("[file-inputs-demo] apps:  apps_upload_image, apps_analyze_documents (in-iframe pickers)")
	log.Printf("[file-inputs-demo] uploads will be written to %s (not auto-cleaned)", uploadDir)
	if err := common.RunServer(common.ServerConfig{
		Name:           "file-inputs-demo",
		Addr:           *addr,
		TracerProvider: tp,
		Wire:           wire,
		Options: []server.Option{
			// MCP Apps extension powers the in-iframe file picker apps
			// registered in apps.go (SEP-2356 Phase 2.1).
			server.WithExtension(&ui.UIExtension{}),
			// SEP-2356 Phase 1.4 — auto-validate file-typed args (size +
			// MIME) against the descriptor declared in each tool's
			// InputSchema. Failures surface as -32602 with structured data.
			server.WithFileInputValidation(),
		},
		Register: func(srv *server.Server) {
			mcp.Register(srv, mcp.Options{UploadDir: uploadDir, Preview: previewFile})
		},
	}); err != nil {
		log.Fatalf("ListenAndServe: %v", err)
	}
}

// registerTools installs the three SEP-2356 demo tools on the server.
