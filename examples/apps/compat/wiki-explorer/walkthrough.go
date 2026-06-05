package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
)

// runDemo is a STUB walkthrough — it connects to the fixture and lists
// its tools. Refine it: add a tools/call step per tool, attach
// common.WireRecipe(step, curl, go) for the wire reproduction, drop
// fixture-specific narrative in .Note(...). See
// examples/apps/compat/basic-vanillajs/walkthrough.go for the full
// pattern.
//
// TODO: replace this stub with a curated walkthrough.
func runDemo() {
	serverURL := serverURLFor3101()

	demo := demokit.New("wiki-explorer walkthrough (stub)").
		Description("TODO: describe what this walkthrough demonstrates.").
		Actors(
			demokit.Actor("Host", "MCP Host (this client)"),
			demokit.Actor("Server", "mcpkit-Go fixture (make serve)"),
		)

	var c *client.Client

	demo.Step("Connect to the fixture").
		Arrow("Host", "Server", "POST /mcp — initialize").
		DashedArrow("Server", "Host", "serverInfo + capabilities + Mcp-Session-Id").
		Note("Stub — refine in walkthrough.go.").
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			c = client.NewClient(serverURL+"/mcp",
				core.ClientInfo{Name: "wiki-explorer-host", Version: "1.0"},
			)
			if err := c.Connect(); err != nil {
				fmt.Printf("    ERROR: %v\n    Start the server with: make serve\n", err)
				return nil
			}
			fmt.Printf("    connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)
			return nil
		})

	demo.Step("List tools").
		Arrow("Host", "Server", "tools/list").
		DashedArrow("Server", "Host", "tools[]").
		Note("Stub — refine in walkthrough.go.").
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			res, err := c.Call("tools/list", map[string]any{})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return nil
			}
			pretty, _ := json.MarshalIndent(json.RawMessage(res.Raw), "    ", "  ")
			fmt.Printf("    %s\n", string(pretty))
			return nil
		})

	common.SetupRenderer(demo)
	demo.Execute()
}

// serverURLFor3101 returns the walkthrough's target MCP server URL.
// Honors $MCPKIT_SERVER_URL as an explicit override; defaults to
// localhost:3101 (the compat-fixture port).
func serverURLFor3101() string {
	if u := os.Getenv(common.ServerURLEnv); u != "" {
		return u
	}
	return "http://localhost:3101"
}
