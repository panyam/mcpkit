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
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
	"github.com/panyam/mcpkit/ext/ui"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/servicekit/middleware"
)

type geocodeInput struct {
	Query string `json:"query"`
}

// nominatimResult mirrors the subset of OSM Nominatim's JSON response
// upstream's server.ts decodes. boundingbox arrives as 4 stringified
// floats in [south, north, west, east] order.
type nominatimResult struct {
	DisplayName string    `json:"display_name"`
	Lat         string    `json:"lat"`
	Lon         string    `json:"lon"`
	BoundingBox [4]string `json:"boundingbox"`
	Type        string    `json:"type"`
	Importance  float64   `json:"importance"`
}

// nominatimRateLimit honours OSM's 1 request/sec usage policy — we
// pad to 1.1s so a clock skew doesn't get us throttled. Single global
// mutex + last-request timestamp matches upstream's shape.
var (
	nominatimMu              sync.Mutex
	nominatimLastRequest     time.Time
	nominatimRateLimitWindow = 1100 * time.Millisecond
)

// geocodeWithNominatim hits OSM's Nominatim search API and returns up
// to 5 results. Throttles to 1.1s between calls. The User-Agent string
// is required by Nominatim's policy. Ported from upstream's
// geocodeWithNominatim() in server.ts.
func geocodeWithNominatim(query string) ([]nominatimResult, error) {
	nominatimMu.Lock()
	if elapsed := time.Since(nominatimLastRequest); elapsed < nominatimRateLimitWindow {
		time.Sleep(nominatimRateLimitWindow - elapsed)
	}
	nominatimLastRequest = time.Now()
	nominatimMu.Unlock()

	q := url.Values{}
	q.Set("q", query)
	q.Set("format", "json")
	q.Set("limit", "5")
	req, err := http.NewRequest("GET", "https://nominatim.openstreetmap.org/search?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "MCP-CesiumMap-Example/1.0 (https://github.com/modelcontextprotocol)")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("nominatim API error: %d %s — %s", resp.StatusCode, resp.Status, body)
	}
	var out []nominatimResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// formatGeocodeResults mirrors upstream's text formatter — the iframe
// doesn't read this (it parses the structured response), but it shows
// up in any tool result viewer (MCPJam Inspector, claude.ai, the
// scripted walkthrough).
func formatGeocodeResults(results []nominatimResult, query string) string {
	if len(results) == 0 {
		return fmt.Sprintf("No results found for %q", query)
	}
	var b strings.Builder
	for i, r := range results {
		lat, _ := strconv.ParseFloat(r.Lat, 64)
		lon, _ := strconv.ParseFloat(r.Lon, 64)
		south, _ := strconv.ParseFloat(r.BoundingBox[0], 64)
		north, _ := strconv.ParseFloat(r.BoundingBox[1], 64)
		west, _ := strconv.ParseFloat(r.BoundingBox[2], 64)
		east, _ := strconv.ParseFloat(r.BoundingBox[3], 64)
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b,
			"%d. %s\n   Coordinates: %.6f, %.6f\n   Bounding box: W:%.4f, S:%.4f, E:%.4f, N:%.4f",
			i+1, r.DisplayName, lat, lon, west, south, east, north)
	}
	return b.String()
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
						URI:      req.URI,
						MimeType: core.AppMIMEType,
						Text:     html,
						// CesiumJS streams its viewer bundle from cesium.com
						// CDN and OSM tiles + Nominatim geocoding hit
						// *.openstreetmap.org. Without this per-content CSP
						// allowlist basic-host's default CSP blocks the
						// fetches — the iframe loads but Cesium fails with
						// "Failed to load CesiumJS from CDN". Mirrors
						// upstream map-server/server.ts's cspMeta block.
						Meta: &core.ResourceContentMeta{
							UI: &core.UIMetadata{
								CSP: &core.UICSPConfig{
									ConnectDomains: []string{
										"https://*.openstreetmap.org",
										"https://cesium.com",
										"https://*.cesium.com",
									},
									ResourceDomains: []string{
										"https://*.openstreetmap.org",
										"https://cesium.com",
										"https://*.cesium.com",
									},
								},
							},
						},
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
				func(ctx core.ToolContext, in geocodeInput) (string, error) {
					results, err := geocodeWithNominatim(in.Query)
					if err != nil {
						return "", fmt.Errorf("geocoding error: %w", err)
					}
					return formatGeocodeResults(results, in.Query), nil
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
