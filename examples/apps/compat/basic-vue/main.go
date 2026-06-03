// Drop-in mcpkit equivalent of upstream's basic-server-vue example.
//
// Same tool surface as upstream (get-time, no output schema — text content
// only) and same ui:// resource URI, so upstream's Playwright suite at
// modelcontextprotocol/ext-apps runs unmodified against this binary.
// Differs from basic-vanillajs only in (a) the server name and (b) the
// upstream example dir whose dist/mcp-app.html we serve verbatim.
//
// Reads upstream's built mcp-app.html from $EXT_APPS_DIR (default
// /tmp/ext-apps) at startup. Fails loudly if not found — caller must have
// cloned upstream and run `npm run build` for basic-server-vue.
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
	htmlPath := filepath.Join(extAppsDir, "examples", "basic-server-vue", "dist", "mcp-app.html")
	htmlBytes, err := os.ReadFile(htmlPath)
	if err != nil {
		log.Fatalf("read %s: %v (set EXT_APPS_DIR and `npm run build` upstream)", htmlPath, err)
	}
	html := string(htmlBytes)

	opts := common.MCPServerOptions(*addr, "[basic-vue] ")
	opts = append(opts, server.WithExtension(&ui.UIExtension{}))
	srv := server.NewServer(
		core.ServerInfo{Name: "Basic MCP App Server (Vue)", Version: "1.0.0"},
		opts...,
	)

	resourceURI := "ui://get-time/mcp-app.html"

	// Out=string so RegisterTypedAppTool emits no outputSchema, matching
	// upstream basic-server-vue's text-only get-time output.
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

	log.Printf("basic-vue compat fixture listening on %s (MCP at /mcp)", *addr)
	log.Printf("serving mcp-app.html from %s (%d bytes)", htmlPath, len(html))
	cors := middleware.CORS(nil,
		middleware.CORSAllowMethods("GET", "POST", "DELETE", "OPTIONS"),
		middleware.CORSAllowHeaders("Content-Type", "Authorization", "Mcp-Session-Id", "Mcp-Protocol-Version"),
		middleware.CORSExposeHeaders("Mcp-Session-Id"),
	)

	if err := srv.Run(*addr, server.WithHandlerWrap(cors)); err != nil {
		log.Fatal(err)
	}
}
