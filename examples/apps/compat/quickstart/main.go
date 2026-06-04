// Drop-in mcpkit equivalent of upstream's quickstart example.
//
// Same tool surface as upstream (get-time, no output schema — text content
// only) and same ui:// resource URI, so upstream's Playwright suite at
// modelcontextprotocol/ext-apps runs unmodified against this binary.
// Differs from basic-vanillajs in (a) the server name, (b) the upstream
// example dir, and (c) no structured output (text content only).
//
// Run:  EXT_APPS_DIR=/tmp/ext-apps PORT=3101 go run .
package main

import (
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
	htmlPath := filepath.Join(extAppsDir, "examples", "quickstart", "dist", "mcp-app.html")
	htmlBytes, err := os.ReadFile(htmlPath)
	if err != nil {
		log.Fatalf("read %s: %v (set EXT_APPS_DIR and `npm run build` upstream)", htmlPath, err)
	}
	html := string(htmlBytes)

	log.Printf("[quickstart] serving mcp-app.html from %s (%d bytes)", htmlPath, len(html))

	cors := middleware.CORS(nil,
		middleware.CORSAllowMethods("GET", "POST", "DELETE", "OPTIONS"),
		middleware.CORSAllowHeaders("Content-Type", "Authorization", "Mcp-Session-Id", "Mcp-Protocol-Version"),
		middleware.CORSExposeHeaders("Mcp-Session-Id"),
	)

	resourceURI := "ui://get-time/mcp-app.html"
	if err := common.RunServer(common.ServerConfig{
		Name:      "Quickstart MCP App Server",
		Version:   "1.0.0",
		Addr:      *addr,
		LogPrefix: "[quickstart] ",
		Options: []server.Option{
			server.WithExtension(&ui.UIExtension{}),
		},
		Register: func(srv *server.Server) {
			// Matches upstream's quickstart registerAppTool: same tool name, same
			// title, slightly shorter description ("Returns the current server
			// time." with no ISO 8601 suffix — drift check enforces this), text-only
			// output (no outputSchema).
			ui.RegisterTypedAppTool(srv, ui.TypedAppToolConfig[struct{}, string]{
				Name:        "get-time",
				Title:       "Get Time",
				Description: "Returns the current server time.",
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
