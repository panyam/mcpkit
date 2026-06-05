// Drop-in mcpkit equivalent of upstream's map-server example.
//
// One tool — show-map — with bounding-box coordinates as numeric inputs
// (all with defaults, all with describe() text that's comma-free).
// Struct tags handle the whole input surface cleanly. Upstream's map
// renders via CesiumJS in the iframe and needs 15s stabilization (which
// upstream's Playwright config already handles).
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

type geocodeInput struct {
	Query string `json:"query"`
}

type showMapInput struct {
	West  float64 `json:"west,omitempty" jsonschema:"default=-0.5,description=Western longitude (-180 to 180)"`
	South float64 `json:"south,omitempty" jsonschema:"default=51.3,description=Southern latitude (-90 to 90)"`
	East  float64 `json:"east,omitempty" jsonschema:"default=0.3,description=Eastern longitude (-180 to 180)"`
	North float64 `json:"north,omitempty" jsonschema:"default=51.7,description=Northern latitude (-90 to 90)"`
	Label string  `json:"label,omitempty" jsonschema:"description=Optional label to display on the map"`
}

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
	htmlPath := filepath.Join(extAppsDir, "examples", "map-server", "dist", "mcp-app.html")
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

	log.Printf("[map] serving mcp-app.html from %s (%d bytes)", htmlPath, len(html))

	resourceURI := "ui://cesium-map/mcp-app.html"
	if err := common.RunServer(common.ServerConfig{
		Name:      "CesiumJS Map Server",
		Version:   "1.0.0",
		Addr:      *addr,
		LogPrefix: "[map] ",
		Options: []server.Option{
			server.WithExtension(&ui.UIExtension{}),
		},
		Register: func(srv *server.Server) {
			// Tool 1: show-map — App tool with its own UI iframe.
			ui.RegisterTypedAppTool(srv, ui.TypedAppToolConfig[showMapInput, string]{
				Name:        "show-map",
				Title:       "Show Map",
				Description: "Display an interactive world map zoomed to a specific bounding box. Use the GeoCode tool to find the bounding box of a location. The widget is interactive and exposes tools for navigation (fly to locations) and querying the current view.",
				Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportForbidden},
				Handler: func(ctx core.ToolContext, in showMapInput) (string, error) {
					// Visual test doesn't depend on the response content; the iframe's
					// CesiumJS does the rendering. Upstream returns a text summary.
					return "Displaying globe.", nil
				},
				ResourceURI: resourceURI,
				ResourceHandler: func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
					return core.ResourceResult{Contents: []core.ResourceReadContent{{
						URI: req.URI, MimeType: core.AppMIMEType, Text: html,
					}}}, nil
				},
			})

			// Tool 2: geocode — plain MCP tool (no UI), called by the App via the
			// bridge. InputSchemaPatch lets us land the comma-rich description
			// without struct-tag truncation; reflection still emits the
			// `type: string` shape.
			geocodeTyped := core.TypedTool[geocodeInput, string](
				"geocode",
				"Search for places using OpenStreetMap. Returns coordinates and bounding boxes for up to 5 matches.",
				func(ctx core.ToolContext, _ geocodeInput) (string, error) {
					return "No results.", nil
				},
				core.WithToolExecution(&core.ToolExecution{TaskSupport: core.TaskSupportForbidden}),
				core.WithInputSchemaPatch(func(s *core.SchemaBuilder) {
					s.Prop("query").
						Desc("Place name or address to search for (e.g., 'Paris', 'Golden Gate Bridge', '1600 Pennsylvania Ave')").
						Required()
				}),
			)
			geocodeTyped.Title = "Geocode"
			srv.RegisterTool(geocodeTyped.ToolDef, geocodeTyped.Handler)
		},
		TransportOptions: []server.TransportOption{
			server.WithHandlerWrap(cors),
		},
	}); err != nil {
		log.Fatal(err)
	}
}
