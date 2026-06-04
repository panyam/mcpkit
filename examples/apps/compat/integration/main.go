// Drop-in mcpkit equivalent of upstream's integration-server example.
//
// Same get-time tool surface as basic-vanillajs (structured output: { time }),
// plus a `resource:///sample-report.txt` downloadable text resource that
// demos the host's ResourceLink + ui/download-file pathway. The three extra
// Playwright interaction tests upstream ships ("Send Message", "Send Log",
// "Open Link") drive the iframe's bridge JS directly — our server's role
// is just to expose the tool + serve the iframe HTML verbatim from upstream.
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
	htmlPath := filepath.Join(extAppsDir, "examples", "integration-server", "dist", "mcp-app.html")
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

	log.Printf("[integration] serving mcp-app.html from %s (%d bytes)", htmlPath, len(html))

	if err := common.RunServer(common.ServerConfig{
		Name:      "Integration Test Server",
		Version:   "1.0.0",
		Addr:      *addr,
		LogPrefix: "[integration] ",
		Options: []server.Option{
			server.WithExtension(&ui.UIExtension{}),
		},
		Register: func(srv *server.Server) {
			registerIntegrationTools(srv, html)
		},
		TransportOptions: []server.TransportOption{
			server.WithHandlerWrap(cors),
		},
	}); err != nil {
		log.Fatal(err)
	}
}

func registerIntegrationTools(srv *server.Server, html string) {
	resourceURI := "ui://get-time/mcp-app.html"
	const sampleDownloadURI = "resource:///sample-report.txt"

	ui.RegisterTypedAppTool(srv, ui.TypedAppToolConfig[struct{}, getTimeOutput]{
		Name:        "get-time",
		Title:       "Get Time",
		Description: "Returns the current server time.",
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

	// Sample downloadable resource. Upstream uses this to demo ResourceLink in
	// ui/download-file. Each read returns a fresh "Generated: <now>" line so
	// the host can verify the round-trip; the content is otherwise static.
	srv.RegisterResource(
		core.ResourceDef{
			URI:      sampleDownloadURI,
			Name:     sampleDownloadURI,
			MimeType: "text/plain",
		},
		func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			content := "Integration Test Server — Sample Report\n" +
				"Generated: " + time.Now().UTC().Format(time.RFC3339Nano) + "\n" +
				"\n" +
				"This file was downloaded via MCP ResourceLink.\n" +
				"The host resolved it by calling resources/read on the server."
			return core.ResourceResult{Contents: []core.ResourceReadContent{{
				URI: req.URI, MimeType: "text/plain", Text: content,
			}}}, nil
		},
	)
}
