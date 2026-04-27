// Example: Dashboard MCP App with tool lifecycle management.
//
// The HTML app registers 5 tools via registerTool() and demonstrates the
// full tool lifecycle: enable, disable, remove, and sendToolListChanged.
// Tools become available/unavailable based on app state.
//
// Run:  go run . -addr :8080
// Connect a host to http://localhost:8080/mcp, ask "open the dashboard".
package main

import (
	"bytes"
	_ "embed"
	"flag"
	"html/template"
	"log"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/ui"
	"github.com/panyam/mcpkit/server"
)

//go:embed dashboard.html
var dashboardTemplateRaw string

type pageData struct {
	Bridge ui.BridgeData
}

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	tmpl := template.Must(template.New("dashboard").Parse(dashboardTemplateRaw))
	template.Must(tmpl.Parse(ui.BridgeTemplateDef()))

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, pageData{
		Bridge: ui.NewBridgeData("dashboard-app", "0.1.0"),
	}); err != nil {
		log.Fatal(err)
	}
	dashboardHTML := buf.String()

	srv := server.NewServer(
		core.ServerInfo{Name: "dashboard-app", Version: "0.1.0"},
		server.WithExtension(&ui.UIExtension{}),
	)

	// Server-side tool: open_dashboard — opens the dashboard UI.
	ui.RegisterTypedAppTool(srv, ui.TypedAppToolConfig[struct{}, string]{
		Name:        "open_dashboard",
		Description: "Open the interactive dashboard with data tools",
		Handler: func(ctx core.ToolContext, _ struct{}) (string, error) {
			return "Dashboard opened. The app provides tools for querying data, filtering, exporting, and settings. Ask the model to query data or check settings.", nil
		},
		ResourceURI: "ui://dashboard/view",
		Visibility:  []core.UIVisibility{core.UIVisibilityModel, core.UIVisibilityApp},
		ResourceHandler: func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{Contents: []core.ResourceReadContent{{
				URI: req.URI, MimeType: core.AppMIMEType, Text: dashboardHTML,
			}}}, nil
		},
	})

	log.Printf("dashboard-app listening on %s (MCP at /mcp)", *addr)
	if err := srv.Run(*addr); err != nil {
		log.Fatal(err)
	}
}
