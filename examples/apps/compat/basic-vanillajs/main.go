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
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
	"github.com/panyam/mcpkit/ext/ui"
	"github.com/panyam/mcpkit/server"
)

// permissiveCORS is sized for the test fixture only — basic-host fetches our
// MCP endpoint from a different localhost port, so the browser needs CORS
// headers exposing Mcp-Session-Id and the standard MCP request headers.
func permissiveCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			origin = "*"
		}
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Mcp-Session-Id, Mcp-Protocol-Version")
		h.Set("Access-Control-Expose-Headers", "Mcp-Session-Id")
		h.Set("Access-Control-Allow-Credentials", "true")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

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

	ui.RegisterTypedAppTool(srv, ui.TypedAppToolConfig[struct{}, getTimeOutput]{
		Name:        "get-time",
		Description: "Returns the current server time as an ISO 8601 string.",
		Handler: func(ctx core.ToolContext, _ struct{}) (getTimeOutput, error) {
			return getTimeOutput{Time: time.Now().UTC().Format(time.RFC3339Nano)}, nil
		},
		ResourceURI: resourceURI,
		Visibility:  []core.UIVisibility{core.UIVisibilityModel, core.UIVisibilityApp},
		ResourceHandler: func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{Contents: []core.ResourceReadContent{{
				URI: req.URI, MimeType: core.AppMIMEType, Text: html,
			}}}, nil
		},
	})

	log.Printf("basic-vanillajs compat fixture listening on %s (MCP at /mcp)", *addr)
	log.Printf("serving mcp-app.html from %s (%d bytes)", htmlPath, len(html))
	if err := srv.Run(*addr, server.WithHandlerWrap(permissiveCORS)); err != nil {
		log.Fatal(err)
	}
}
