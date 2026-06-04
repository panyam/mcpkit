// Drop-in mcpkit equivalent of upstream's pdf-server example with the
// `--enable-interact` surface enabled by default — 9 tools, command
// queue, long-poll, viewer rendezvous. Companion files:
//
//	queue.go      per-viewUUID command queue + long-poll waiter + submit_* rendezvous
//	bytes.go      HTTP range / local-file proxy for read_pdf_bytes
//	tools.go      all 9 tool registrations + interact action dispatcher
//
// Target test surface (issue 554):
//
//	servers.spec.ts                  2 standard tests (loads app UI, screenshot)
//	pdf-annotations.spec.ts          8 tests
//	pdf-incremental-load.spec.ts     3 tests (form-field probe omitted —
//	                                 see PR description for the tradeoff)
//	pdf-viewer-zoom.spec.ts          4 tests (mostly iframe-side)
//	pdf-annotations-api.spec.ts      3 tests, LLM-gated, auto-skip
//
// Run: EXT_APPS_DIR=/tmp/ext-apps PORT=3101 go run .
package main

import (
	"flag"
	"log"
	"os"
	"path/filepath"

	"github.com/panyam/mcpkit/examples/common"
	"github.com/panyam/mcpkit/ext/ui"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/servicekit/middleware"
)

const (
	defaultPDF      = "https://arxiv.org/pdf/1706.03762"
	maxChunkBytes   = 512 * 1024
	resourceURI     = "ui://pdf-viewer/mcp-app.html"
	pdfDisplayName  = "PDF Server"
	pdfServerVer    = "2.0.0"
	defaultLogTag   = "[pdf-server] "
	defaultListenOn = "3101"
)

func main() {
	defaultPort := defaultListenOn
	if p := os.Getenv("PORT"); p != "" {
		defaultPort = p
	}
	addr := flag.String("addr", ":"+defaultPort, "listen address")
	flag.Parse()

	extAppsDir := os.Getenv("EXT_APPS_DIR")
	if extAppsDir == "" {
		extAppsDir = "/tmp/ext-apps"
	}
	htmlPath := filepath.Join(extAppsDir, "examples", "pdf-server", "dist", "mcp-app.html")
	htmlBytes, err := os.ReadFile(htmlPath)
	if err != nil {
		log.Fatalf("read %s: %v (set EXT_APPS_DIR and `npm run build` upstream)", htmlPath, err)
	}
	html := string(htmlBytes)

	cors := middleware.CORS(nil,
		middleware.CORSAllowMethods("GET", "POST", "DELETE", "OPTIONS"),
		middleware.CORSAllowHeaders("Content-Type", "Authorization", "Mcp-Session-Id", "Mcp-Protocol-Version"),
		middleware.CORSExposeHeaders("Mcp-Session-Id"),
	)

	log.Printf("%sserving mcp-app.html from %s (%d bytes)", defaultLogTag, htmlPath, len(html))

	hub := newHub()
	if err := common.RunServer(common.ServerConfig{
		Name:      pdfDisplayName,
		Version:   pdfServerVer,
		Addr:      *addr,
		LogPrefix: defaultLogTag,
		Options: []server.Option{
			server.WithExtension(&ui.UIExtension{}),
		},
		Register: func(srv *server.Server) {
			registerAllTools(srv, hub, html)
		},
		TransportOptions: []server.TransportOption{
			server.WithHandlerWrap(cors),
		},
	}); err != nil {
		log.Fatal(err)
	}
}
