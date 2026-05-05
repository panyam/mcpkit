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
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
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

	opts := common.MCPServerOptions(*addr, "[mcp] ")
	opts = append(opts, server.WithExtension(&ui.UIExtension{}))
	srv := server.NewServer(
		core.ServerInfo{Name: "React MCP App", Version: "1.0.0"},
		opts...,
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

	// --- Elicitation demo: timezone picker ---
	srv.Register(core.TextTool[struct{}]("get-time-with-tz",
		"Returns the current time in a user-selected timezone — asks the user to pick via elicitation",
		func(ctx core.ToolContext, _ struct{}) (string, error) {
			result, err := ctx.Elicit(core.ElicitationRequest{
				Message: "Which timezone would you like the time in?",
				RequestedSchema: json.RawMessage(`{
					"type": "object",
					"properties": {
						"timezone": {
							"type": "string",
							"enum": ["UTC", "US/Eastern", "US/Pacific", "Europe/London", "Asia/Tokyo", "Australia/Sydney"],
							"default": "UTC",
							"description": "Timezone"
						}
					}
				}`),
			})
			if err != nil {
				return fmt.Sprintf("Elicitation failed: %v", err), nil
			}
			if result.Action != "accept" {
				return fmt.Sprintf("Cancelled (action=%s)", result.Action), nil
			}
			tz, _ := result.Content["timezone"].(string)
			if tz == "" {
				tz = "UTC"
			}
			loc, err := time.LoadLocation(tz)
			if err != nil {
				return fmt.Sprintf("Invalid timezone %q: %v", tz, err), nil
			}
			return time.Now().In(loc).Format(time.RFC3339), nil
		},
	))

	// --- Sampling demo: fun fact about today ---
	srv.Register(core.TextTool[struct{}]("time-fact",
		"Uses the LLM to generate a fun fact about today's date",
		func(ctx core.ToolContext, _ struct{}) (string, error) {
			now := time.Now().UTC()
			result, err := ctx.Sample(core.CreateMessageRequest{
				Messages: []core.SamplingMessage{{
					Role: "user",
					Content: core.Content{
						Type: "text",
						Text: fmt.Sprintf(
							"Tell me one interesting fun fact about this date: %s. "+
								"Keep it to 1-2 sentences.", now.Format("January 2")),
					},
				}},
				MaxTokens: 100,
			})
			if err != nil {
				return fmt.Sprintf("Sampling failed: %v", err), nil
			}
			return fmt.Sprintf("%s\n\n(via %s)", result.Content.Text, result.Model), nil
		},
	))

	// --- Prompt demo: time format ---
	srv.RegisterPrompt(
		core.PromptDef{
			Name:        "time_format",
			Description: "Ask the LLM to tell you the current time in a specific timezone",
			Arguments: []core.PromptArgument{
				{Name: "timezone", Description: "Timezone (e.g., UTC, US/Eastern)", Required: false},
			},
		},
		func(ctx core.PromptContext, req core.PromptRequest) (core.PromptResult, error) {
			tz := "UTC"
			if v, ok := req.Arguments["timezone"]; ok && v != "" {
				tz, _ = v.(string)
				if tz == "" {
					tz = "UTC"
				}
			}
			return core.PromptResult{
				Description: fmt.Sprintf("Time in %s", tz),
				Messages: []core.PromptMessage{{
					Role: "user",
					Content: core.Content{
						Type: "text",
						Text: fmt.Sprintf("What is the current time in %s? Format it nicely.", tz),
					},
				}},
			}, nil
		},
	)

	log.Printf("react-app listening on %s (MCP at /mcp)", *addr)
	if err := srv.Run(*addr); err != nil {
		log.Fatal(err)
	}
}
