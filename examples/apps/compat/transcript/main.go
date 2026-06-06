// Drop-in mcpkit equivalent of upstream's transcript-server example.
//
// Exposes a "transcribe" tool that opens a live speech transcription UI
// via Web Speech API (handled entirely in the iframe HTML; the server's
// role is to acknowledge the call). Server returns a fixed JSON status
// payload as text content.
//
// Run:  EXT_APPS_DIR=/tmp/ext-apps PORT=3101 go run .
package main

import (
	"strings"
	"flag"
	"log"
	"os"
	"path/filepath"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
	"github.com/panyam/mcpkit/ext/ui"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/servicekit/middleware"
)

// transcribeReady is the fixed JSON-stringified status upstream's transcript-
// server returns as text content on every transcribe call. Mirrored verbatim
// so the iframe app sees the same payload regardless of which backend served
// it.
const transcribeReady = `{"status":"ready","message":"Transcription UI opened. Speak into your microphone."}`

func main() {
	// Dual-mode dispatcher: `--demo` runs the demokit walkthrough (acts as
	// an MCP client against a running server in another terminal). Default
	// (no flag) keeps the existing server behaviour so apps_demo.py and
	// the Playwright wrapper continue to work unchanged.
	for _, arg := range os.Args[1:] {
		if strings.TrimSpace(arg) == "--demo" {
			runDemo()
			return
		}
	}
	serve()
}

func serve() {
	defaultPort := "3101"
	if p := os.Getenv("PORT"); p != "" {
		defaultPort = p
	}
	addr := flag.String("addr", ":"+defaultPort, "listen address")
	flag.Parse()

	extAppsDir := os.Getenv("EXT_APPS_DIR")
	if extAppsDir == "" {
		extAppsDir = "/tmp/ext-apps"
	}
	htmlPath := filepath.Join(extAppsDir, "examples", "transcript-server", "dist", "mcp-app.html")
	htmlBytes, err := os.ReadFile(htmlPath)
	if err != nil {
		log.Fatalf("read %s: %v (set EXT_APPS_DIR and `npm run build` upstream)", htmlPath, err)
	}
	html := string(htmlBytes)

	log.Printf("[transcript] serving mcp-app.html from %s (%d bytes)", htmlPath, len(html))

	cors := middleware.CORS(nil,
		middleware.CORSAllowMethods("GET", "POST", "DELETE", "OPTIONS"),
		middleware.CORSAllowHeaders("Content-Type", "Authorization", "Mcp-Session-Id", "Mcp-Protocol-Version"),
		middleware.CORSExposeHeaders("Mcp-Session-Id"),
	)

	// Resource URI matches upstream's transcript-server (not the get-time
	// pattern the basic-* cluster uses).
	resourceURI := "ui://transcript/mcp-app.html"
	if err := common.RunServer(common.ServerConfig{
		Name:      "Transcript Server",
		Version:   "1.0.0",
		Addr:      *addr,
		LogPrefix: "[transcript] ",
		Options: []server.Option{
			server.WithExtension(&ui.UIExtension{}),
		},
		Register: func(srv *server.Server) {
			ui.RegisterTypedAppTool(srv, ui.TypedAppToolConfig[struct{}, string]{
				Name:        "transcribe",
				Title:       "Transcribe Speech",
				Description: "Opens a live speech transcription interface using the Web Speech API.",
				Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportForbidden},
				Handler: func(ctx core.ToolContext, _ struct{}) (string, error) {
					return transcribeReady, nil
				},
				ResourceURI: resourceURI,
				ResourceHandler: func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
					return core.ResourceResult{Contents: []core.ResourceReadContent{{
						URI:      req.URI,
						MimeType: core.AppMIMEType,
						Text:     html,
						// Spec puts iframe Permission-Policy capabilities on the
						// resource's _meta.ui.permissions — hosts read this to set
						// the `<iframe allow=...>` attribute. The transcript App
						// needs microphone (Web Speech API) and clipboardWrite
						// (its copy-transcript button). Without this _meta block,
						// basic-host renders the iframe with no policy grant and
						// recognition.start() silently fails.
						Meta: &core.ResourceContentMeta{
							UI: &core.UIMetadata{
								Permissions: &core.UIPermissions{
									Microphone:     &struct{}{},
									ClipboardWrite: &struct{}{},
								},
							},
						},
					}}}, nil
				},
			})
		},
		TransportOptions: []server.TransportOption{
			server.WithHandlerWrap(cors),
		},
	}); err != nil {
		log.Fatal(err)
	}
}
