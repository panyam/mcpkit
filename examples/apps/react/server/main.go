// Go server for the React MCP App example.
//
// Mirrors the upstream ext-apps basic-server-vanillajs but with:
// - mcpkit Go backend instead of TypeScript
// - React frontend using our bridge + useMCPApp hook
// - Vite single-file build served as MCP App resource
//
// Build first:  cd .. && pnpm install && pnpm build
// Run:          go run . -addr :8080
// Connect MCPJam to http://localhost:8080/mcp.
package main

import (
	"flag"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/ui"
	"github.com/panyam/mcpkit/server"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	// Read the Vite-built HTML. Try multiple paths since `go run .`
	// can be invoked from the server/ dir or the react/ dir.
	candidates := []string{
		filepath.Join("..", "dist", "index.html"),  // from server/
		filepath.Join("dist", "index.html"),         // from react/
	}
	var htmlBytes []byte
	for _, p := range candidates {
		if b, err := os.ReadFile(p); err == nil {
			htmlBytes = b
			break
		}
	}
	if htmlBytes == nil {
		log.Fatal("Run 'pnpm build' in the react/ directory first: dist/index.html not found")
	}

	// Inject the bridge into the Vite-built HTML.
	appHTML := ui.InjectAppBridge(string(htmlBytes), &ui.BridgeConfig{
		Name:    "react-app",
		Version: "0.1.0",
	})

	srv := server.NewServer(
		core.ServerInfo{Name: "React MCP App", Version: "1.0.0"},
		server.WithExtension(&ui.UIExtension{}),
	)

	// get-time tool — matches upstream basic-server-vanillajs.
	ui.RegisterTypedAppTool(srv, ui.TypedAppToolConfig[struct{}, core.ToolResult]{
		Name:        "get-time",
		Description: "Returns the current server time as an ISO 8601 string.",
		Handler: func(ctx core.ToolContext, _ struct{}) (core.ToolResult, error) {
			t := time.Now().UTC().Format(time.RFC3339)
			return core.StructuredResult(t, map[string]string{"time": t}), nil
		},
		ResourceURI: "ui://get-time/react-app",
		Visibility:  []core.UIVisibility{core.UIVisibilityModel, core.UIVisibilityApp},
		ResourceHandler: func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{Contents: []core.ResourceReadContent{{
				URI: req.URI, MimeType: core.AppMIMEType, Text: appHTML,
			}}}, nil
		},
	})

	log.Printf("react-app listening on %s (MCP at /mcp)", *addr)
	if err := srv.Run(*addr); err != nil {
		log.Fatal(err)
	}
}
