// Drop-in mcpkit equivalent of upstream's wiki-explorer-server example.
//
// One tool — get-first-degree-links — that takes a Wikipedia URL and
// returns its outgoing links. Default URL is comma-free so struct tags
// handle the input cleanly. Output is a structured graph payload upstream
// renders as a force-directed graph in the iframe; visual test masks
// the dynamic graph layout, so our stub returns empty arrays.
//
// Run:  EXT_APPS_DIR=/tmp/ext-apps PORT=3101 go run .
package main

import (
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

type wikiInput struct {
	URL string `json:"url,omitempty" jsonschema:"format=uri,default=https://en.wikipedia.org/wiki/Model_Context_Protocol,description=Wikipedia page URL"`
}

type wikiPage struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

type wikiLinksOutput struct {
	Page  wikiPage   `json:"page"`
	Links []wikiPage `json:"links"`
	// Error mirrors upstream's `z.string().nullable()` — value is either a
	// string or null. Reflection can't produce the matching JSON Schema
	// (`{"type": ["string", "null"]}`), so the OutputSchemaOverride on the
	// tool config supplies the exact shape. Handler returns *string: nil
	// serializes to JSON null; a non-nil pointer serializes to the string.
	Error *string `json:"error"`
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
	htmlPath := filepath.Join(extAppsDir, "examples", "wiki-explorer-server", "dist", "mcp-app.html")
	htmlBytes, err := os.ReadFile(htmlPath)
	if err != nil {
		log.Fatalf("read %s: %v (set EXT_APPS_DIR and `npm run build` upstream)", htmlPath, err)
	}
	html := string(htmlBytes)

	opts := common.MCPServerOptions(*addr, "[wiki-explorer] ")
	opts = append(opts, server.WithExtension(&ui.UIExtension{}))
	srv := server.NewServer(
		core.ServerInfo{Name: "Wiki Explorer", Version: "1.0.0"},
		opts...,
	)

	resourceURI := "ui://wiki-explorer/mcp-app.html"

	ui.RegisterTypedAppTool(srv, ui.TypedAppToolConfig[wikiInput, wikiLinksOutput]{
		Name:        "get-first-degree-links",
		Title:       "Get First-Degree Links",
		Description: "Returns all Wikipedia pages that the given page links to directly. The widget is interactive and exposes tools for exploring the graph (expanding nodes to see their links), searching for articles, and querying visible nodes.",
		Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportForbidden},
		// Reflection covers page + links cleanly from the Go struct; only
		// the nullable `error` field needs help. Upstream's `z.string()
		// .nullable()` emits an anyOf — Go's `*string` reflects to plain
		// `"type": "string"`. Patch.Replace lands the matching shape.
		OutputSchemaPatch: func(s *core.SchemaBuilder) {
			s.Prop("error").Replace(map[string]any{
				"anyOf": []any{
					map[string]any{"type": "string"},
					map[string]any{"type": "null"},
				},
			})
		},
		Handler: func(ctx core.ToolContext, in wikiInput) (wikiLinksOutput, error) {
			// Visual test masks the graph (HOST_MASKS["wiki-explorer"] = ["#graph"]).
			// Stub response — iframe renders its own layout.
			return wikiLinksOutput{
				Page:  wikiPage{URL: in.URL, Title: "Model Context Protocol"},
				Links: []wikiPage{},
				Error: nil, // serializes to JSON null
			}, nil
		},
		ResourceURI: resourceURI,
		ResourceHandler: func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{Contents: []core.ResourceReadContent{{
				URI: req.URI, MimeType: core.AppMIMEType, Text: html,
			}}}, nil
		},
	})

	cors := middleware.CORS(nil,
		middleware.CORSAllowMethods("GET", "POST", "DELETE", "OPTIONS"),
		middleware.CORSAllowHeaders("Content-Type", "Authorization", "Mcp-Session-Id", "Mcp-Protocol-Version"),
		middleware.CORSExposeHeaders("Mcp-Session-Id"),
	)

	log.Printf("wiki-explorer compat fixture listening on %s (MCP at /mcp)", *addr)
	log.Printf("serving mcp-app.html from %s (%d bytes)", htmlPath, len(html))
	if err := srv.Run(*addr, server.WithHandlerWrap(cors)); err != nil {
		log.Fatal(err)
	}
}
