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
	"strings"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
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

	opts := common.MCPServerOptions(*addr, "[mcp] ")
	opts = append(opts, server.WithExtension(&ui.UIExtension{}))
	srv := server.NewServer(
		core.ServerInfo{Name: "dice-app", Version: "0.1.0"},
		opts...,
	)

	type rollDiceInput struct {
		Sides int `json:"sides,omitempty" jsonschema:"description=Number of sides on the die,default=6"`
	}
	ui.RegisterTypedAppTool(srv, ui.TypedAppToolConfig[rollDiceInput, string]{
		Name:        "roll_dice",
		Description: "Roll a die and show the result in a rich UI",
		Handler: func(ctx core.ToolContext, input rollDiceInput) (string, error) {
			if input.Sides <= 0 {
				input.Sides = 6
			}
			result := rand.Intn(input.Sides) + 1
			return fmt.Sprintf("Rolled a d%d: %d", input.Sides, result), nil
		},
		ResourceURI: "ui://dice/view",
		Visibility:  []core.UIVisibility{core.UIVisibilityModel, core.UIVisibilityApp},
		ResourceHandler: func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			result := core.ResourceResult{Contents: []core.ResourceReadContent{{
				URI: req.URI, MimeType: core.AppMIMEType, Text: diceHTML,
			}}}
			// Debug: check if <script> tags survive serialization
			raw, _ := core.MarshalJSON(result)
			s := string(raw)
			if idx := strings.Index(s, "script"); idx > 0 {
				start := idx - 10
				if start < 0 {
					start = 0
				}
				end := idx + 50
				if end > len(s) {
					end = len(s)
				}
				log.Printf("DEBUG script context: ...%s...", s[start:end])
			}
			return result, nil
		},
	})

	log.Printf("dice-app listening on %s (MCP at /mcp)", *addr)
	if err := srv.Run(*addr); err != nil {
		log.Fatal(err)
	}
}
