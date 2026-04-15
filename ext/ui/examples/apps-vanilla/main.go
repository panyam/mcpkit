// Example: Vanilla JS MCP App with mcpkit bridge.
//
// dice.html uses {{ template "mcpkit-bridge" .Bridge }} to include the
// bridge explicitly — like any other dependency. No injection, no magic.
//
// Run:  go run . -addr :8080
// Connect MCPJam to http://localhost:8080/mcp, ask "roll a die".
package main

import (
	"bytes"
	_ "embed"
	"flag"
	"fmt"
	"html/template"
	"log"
	"math/rand"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/ui"
	"github.com/panyam/mcpkit/server"
)

//go:embed dice.html
var diceTemplateRaw string

// pageData is the template data for dice.html.
type pageData struct {
	Bridge ui.BridgeData
}

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	// Parse dice.html + the bridge template definition.
	tmpl := template.Must(template.New("dice").Parse(diceTemplateRaw))
	template.Must(tmpl.Parse(ui.BridgeTemplateDef()))

	// Pre-render the page with bridge baked in.
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, pageData{
		Bridge: ui.NewBridgeData("dice-app", "0.1.0"),
	}); err != nil {
		log.Fatal(err)
	}
	diceHTML := buf.String()

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
				URI: req.URI, MimeType: core.AppMIMEType, Text: diceHTML,
			}}}, nil
		},
	})

	log.Printf("dice-app listening on %s (MCP at /mcp)", *addr)
	if err := srv.Run(*addr); err != nil {
		log.Fatal(err)
	}
}
