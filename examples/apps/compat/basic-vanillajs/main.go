// Drop-in mcpkit equivalent of upstream's basic-server-vanillajs example.
//
// Exposes the same tool name ("get-time"), output schema ({ time: ISO 8601 }),
// and UI resource URI ("ui://get-time/mcp-app.html") as upstream's TS server,
// so upstream's Playwright suite at modelcontextprotocol/ext-apps can be run
// against this binary as a compatibility check.
//
// Reads upstream's built mcp-app.html from $EXT_APPS_DIR (default /tmp/ext-apps)
// at startup. Fails loudly if not found — caller must have cloned upstream and
// run `npm run build` for basic-server-vanillajs.
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

type getTimeOutput struct {
	Time string `json:"time"`
}

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
	htmlPath := filepath.Join(extAppsDir, "examples", "basic-server-vanillajs", "dist", "mcp-app.html")
	htmlBytes, err := os.ReadFile(htmlPath)
	if err != nil {
		log.Fatalf("read %s: %v (set EXT_APPS_DIR and `npm run build` upstream)", htmlPath, err)
	}
	html := string(htmlBytes)

	opts := common.MCPServerOptions(*addr, "[basic-vanillajs] ")
	opts = append(opts, server.WithExtension(&ui.UIExtension{}))
	srv := server.NewServer(
		core.ServerInfo{Name: "Basic MCP App Server (Vanilla JS)", Version: "1.0.0"},
		opts...,
	)

	resourceURI := "ui://get-time/mcp-app.html"

	// Fields match upstream basic-server-vanillajs's registerAppTool call
	// byte-for-byte (Title, Description, Execution.TaskSupport=forbidden).
	// Visibility intentionally NOT set — upstream doesn't emit
	// _meta.ui.visibility either.
	ui.RegisterTypedAppTool(srv, ui.TypedAppToolConfig[struct{}, getTimeOutput]{
		Name:        "get-time",
		Title:       "Get Time",
		Description: "Returns the current server time as an ISO 8601 string.",
		Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportForbidden},
		Handler: func(ctx core.ToolContext, _ struct{}) (getTimeOutput, error) {
			return getTimeOutput{Time: time.Now().UTC().Format(time.RFC3339Nano)}, nil
		},
		ResourceURI: resourceURI,
		ResourceHandler: func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{Contents: []core.ResourceReadContent{{
				URI: req.URI, MimeType: core.AppMIMEType, Text: html,
			}}}, nil
		},
	})

	log.Printf("basic-vanillajs compat fixture listening on %s (MCP at /mcp)", *addr)
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
