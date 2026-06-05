// Drop-in mcpkit equivalent of upstream's basic-server-solid example.
//
// Same tool surface as upstream (get-time, no output schema — text content
// only) and same ui:// resource URI, so upstream's Playwright suite at
// modelcontextprotocol/ext-apps runs unmodified against this binary.
// Differs from basic-vanillajs only in (a) the server name and (b) the
// upstream example dir whose dist/mcp-app.html we serve verbatim.
//
// Reads upstream's built mcp-app.html from $EXT_APPS_DIR (default
// /tmp/ext-apps) at startup. Fails loudly if not found — caller must have
// cloned upstream and run `npm run build` for basic-server-solid.
//
// Run:  EXT_APPS_DIR=/tmp/ext-apps PORT=3101 go run .
package main

import (
	"strings"
	"flag"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
	"github.com/panyam/mcpkit/ext/ui"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/servicekit/middleware"
)

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
	htmlPath := filepath.Join(extAppsDir, "examples", "basic-server-solid", "dist", "mcp-app.html")
	htmlBytes, err := os.ReadFile(htmlPath)
	if err != nil {
		log.Fatalf("read %s: %v (set EXT_APPS_DIR and `npm run build` upstream)", htmlPath, err)
	}
	html := string(htmlBytes)

	log.Printf("[basic-solid] serving mcp-app.html from %s (%d bytes)", htmlPath, len(html))

	cors := middleware.CORS(nil,
		middleware.CORSAllowMethods("GET", "POST", "DELETE", "OPTIONS"),
		middleware.CORSAllowHeaders("Content-Type", "Authorization", "Mcp-Session-Id", "Mcp-Protocol-Version"),
		middleware.CORSExposeHeaders("Mcp-Session-Id"),
	)

	resourceURI := "ui://get-time/mcp-app.html"
	if err := common.RunServer(common.ServerConfig{
		Name:      "Basic MCP App Server (Solid)",
		Version:   "1.0.0",
		Addr:      *addr,
		LogPrefix: "[basic-solid] ",
		Options: []server.Option{
			server.WithExtension(&ui.UIExtension{}),
		},
		Register: func(srv *server.Server) {
			// Out=string so RegisterTypedAppTool emits no outputSchema, matching
			// upstream basic-server-solid's text-only get-time output.
			ui.RegisterTypedAppTool(srv, ui.TypedAppToolConfig[struct{}, string]{
				Name:        "get-time",
				Title:       "Get Time",
				Description: "Returns the current server time as an ISO 8601 string.",
				Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportForbidden},
				Handler: func(ctx core.ToolContext, _ struct{}) (string, error) {
					return time.Now().UTC().Format(time.RFC3339Nano), nil
				},
				ResourceURI: resourceURI,
				ResourceHandler: func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
					return core.ResourceResult{Contents: []core.ResourceReadContent{{
						URI: req.URI, MimeType: core.AppMIMEType, Text: html,
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
