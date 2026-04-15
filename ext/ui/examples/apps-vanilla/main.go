// Example: Vanilla JS MCP App with mcpkit bridge.
//
// A minimal MCP server with one tool ("roll_dice") that returns a random
// number, and an MCP App that displays it. The iframe has a "Roll Again"
// button that calls the tool back through the bridge.
//
// Run:
//
//	go run . -addr :8080
//
// Then connect with Claude Desktop, MCPJam, or any MCP Apps-capable host.
package main

import (
	_ "embed"
	"flag"
	"fmt"
	"log"
	"math/rand"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/ui"
	"github.com/panyam/mcpkit/server"
)

//go:embed dice.html
var diceHTML string

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	// Inject the bridge into our HTML at startup.
	dicePageHTML := ui.InjectAppBridge(diceHTML, &ui.BridgeConfig{
		Name:    "dice-app",
		Version: "0.1.0",
	})

	srv := server.NewServer(
		core.ServerInfo{Name: "dice-app", Version: "0.1.0"},
		server.WithExtension(&ui.UIExtension{}),
	)

	ui.RegisterAppTool(srv, ui.AppToolConfig{
		Name:        "roll_dice",
		Description: "Roll a die and show the result in a rich UI",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"sides": map[string]any{
					"type":        "number",
					"description": "Number of sides on the die",
					"default":     6,
				},
			},
		},
		ResourceURI: "ui://dice/view",
		Visibility:  []core.UIVisibility{core.UIVisibilityModel, core.UIVisibilityApp},
		CSP: &core.UICSPConfig{
			// Allow the host to serve extracted scripts from its own proxy.
			// MCPJam extracts inline <script> blocks and re-serves them
			// through localhost — this must be in script-src.
			ResourceDomains: []string{"'self'"},
		},
		ToolHandler: func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			var args struct {
				Sides int `json:"sides"`
			}
			if err := req.Bind(&args); err != nil || args.Sides <= 0 {
				args.Sides = 6
			}
			result := rand.Intn(args.Sides) + 1
			return core.TextResult(fmt.Sprintf("Rolled a d%d: %d", args.Sides, result)), nil
		},
		ResourceHandler: func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{Contents: []core.ResourceReadContent{{
				URI: req.URI, MimeType: core.AppMIMEType, Text: dicePageHTML,
			}}}, nil
		},
	})

	log.Printf("dice-app listening on %s", *addr)
	if err := srv.Run(*addr); err != nil {
		log.Fatal(err)
	}
}
