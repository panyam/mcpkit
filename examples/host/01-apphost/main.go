// 01-apphost demonstrates AppHost — the mediator between an MCP Client
// (connected to a server) and an AppBridge (connected to an app).
//
// Everything runs in-process: a real MCP server, a real client, and an
// InProcessAppBridge simulating the app side. Run interactively to step
// through each operation, or with --non-interactive for full output.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/host/refs"
	ui "github.com/panyam/mcpkit/ext/ui"
	"github.com/panyam/mcpkit/server"
)

func main() {
	demo := demokit.New("AppHost — Host-Side App Management").
		Dir("01-apphost").
		RunPrefix("examples/host").
		Description("Demonstrates AppHost mediating between an MCP server and an app bridge with bidirectional tool calls.").
		Actors(
			demokit.Actor("Srv", "MCP Server"),
			demokit.Actor("Client", "MCP Client"),
			demokit.Actor("Host", "AppHost"),
			demokit.Actor("Bridge", "InProcessAppBridge"),
		)

	// Shared state across steps.
	var (
		srv          *server.Server
		c            *client.Client
		bridge       *ui.InProcessAppBridge
		host         *ui.AppHost
		ctx          = context.Background()
	)

	// --- Step 1: Create MCP server ---
	demo.Step("Create MCP server with tools").
		Ref(refs.MCPSpec).
		Arrow("Srv", "Srv", "RegisterTool(\"server_echo\")").
		Arrow("Srv", "Srv", "RegisterTool(\"server_time\")").
		Note("The server provides two tools: echo (returns input) and time (returns current time).").
		Run(func() {
			srv = server.NewServer(core.ServerInfo{Name: "demo-server", Version: "1.0"})

			srv.RegisterTool(
				core.ToolDef{Name: "server_echo", Description: "Echo back the input", InputSchema: map[string]any{
					"type": "object", "properties": map[string]any{"msg": map[string]any{"type": "string"}},
				}},
				func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
					var args struct{ Msg string `json:"msg"` }
					req.Bind(&args)
					return core.TextResult("echo: " + args.Msg), nil
				},
			)
			srv.RegisterTool(
				core.ToolDef{Name: "server_time", Description: "Get current time", InputSchema: map[string]any{"type": "object"}},
				func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
					return core.TextResult(time.Now().Format(time.RFC3339)), nil
				},
			)
			fmt.Println("  Server created with 2 tools: server_echo, server_time")
		})

	// --- Step 2: Connect client ---
	demo.Step("Connect client to server via in-process transport").
		Arrow("Client", "Srv", "initialize").
		DashedArrow("Srv", "Client", "capabilities, serverInfo").
		Note("The client connects without HTTP — using InProcessTransport for direct dispatch.").
		Run(func() {
			xport := server.NewInProcessTransport(srv)
			c = client.NewClient("memory://", core.ClientInfo{Name: "demo-host", Version: "1.0"},
				client.WithTransport(xport),
				client.WithUIExtension(),
			)
			if err := c.Connect(); err != nil {
				fmt.Printf("  ERROR: %v\n", err)
				return
			}
			fmt.Printf("  Connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)

			tools, _ := c.ListTools()
			fmt.Printf("  Server tools: ")
			for i, t := range tools {
				if i > 0 {
					fmt.Print(", ")
				}
				fmt.Print(t.Name)
			}
			fmt.Println()
		})

	// --- Step 3: Create app bridge with tools ---
	demo.Step("Create InProcessAppBridge with app-provided tools").
		Ref(refs.MCPAppsSpec).
		Arrow("Bridge", "Bridge", "RegisterTool(\"app_greet\")").
		Arrow("Bridge", "Bridge", "RegisterTool(\"app_counter\")").
		Note("The bridge simulates an MCP App (iframe). It registers two tools that the host/model can call directly.").
		Run(func() {
			bridge = ui.NewInProcessAppBridge()

			bridge.RegisterTool("app_greet", core.ToolDef{
				Description: "Greet someone by name",
				InputSchema: map[string]any{
					"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}},
				},
			}, func(args map[string]any) (any, error) {
				name, _ := args["name"].(string)
				return core.ToolResult{
					Content: []core.Content{{Type: "text", Text: "Hello, " + name + "!"}},
				}, nil
			})

			counter := 0
			bridge.RegisterTool("app_counter", core.ToolDef{
				Description: "Increment and return a counter",
			}, func(args map[string]any) (any, error) {
				counter++
				return core.ToolResult{
					Content: []core.Content{{Type: "text", Text: fmt.Sprintf("counter = %d", counter)}},
				}, nil
			})

			fmt.Println("  Bridge created with 2 app tools: app_greet, app_counter")
		})

	// --- Step 4: Create and start AppHost ---
	demo.Step("Create AppHost and wire everything together").
		Arrow("Host", "Bridge", "SetRequestHandler (app→host)").
		Arrow("Host", "Bridge", "SetNotificationHandler (list_changed)").
		Arrow("Host", "Bridge", "Start()").
		Arrow("Host", "Bridge", "Send(tools/list) — initial fetch").
		DashedArrow("Bridge", "Host", "{tools: [app_greet, app_counter]}").
		Note("AppHost wires up bidirectional routing and fetches the initial app tool list.").
		Run(func() {
			host = ui.NewAppHost(c, bridge)
			if err := host.Start(ctx); err != nil {
				fmt.Printf("  ERROR: %v\n", err)
				return
			}
			fmt.Println("  AppHost started — bridge handlers wired, initial tool list fetched")
		})

	// --- Step 5: List all tools ---
	demo.Step("ListAllTools — aggregated server + app tools").
		Arrow("Host", "Client", "ListTools() — server tools").
		DashedArrow("Client", "Host", "[server_echo, server_time]").
		Arrow("Host", "Bridge", "cached app tools").
		DashedArrow("Bridge", "Host", "[app_greet, app_counter]").
		Note("ListAllTools merges tools from the MCP server and the app bridge into a single list.").
		Run(func() {
			tools, err := host.ListAllTools(ctx)
			if err != nil {
				fmt.Printf("  ERROR: %v\n", err)
				return
			}
			fmt.Printf("  All tools (%d total):\n", len(tools))
			for _, t := range tools {
				fmt.Printf("    - %s (%s)\n", t.Name, t.Description)
			}
		})

	// --- Step 6: Call app tool ---
	demo.Step("CallAppTool — host invokes an app-provided tool").
		Arrow("Host", "Bridge", "Send(tools/call, {name: \"app_greet\", args: {name: \"World\"}})").
		DashedArrow("Bridge", "Host", "ToolResult {text: \"Hello, World!\"}").
		Note("The host calls a tool registered by the app. The bridge dispatches to the Go handler.").
		Run(func() {
			result, err := host.CallAppTool(ctx, "app_greet", map[string]any{"name": "World"})
			if err != nil {
				fmt.Printf("  ERROR: %v\n", err)
				return
			}
			fmt.Printf("  Result: %s\n", result.Content[0].Text)

			// Call counter twice to show state.
			host.CallAppTool(ctx, "app_counter", nil)
			result2, _ := host.CallAppTool(ctx, "app_counter", nil)
			fmt.Printf("  Counter after 2 calls: %s\n", result2.Content[0].Text)
		})

	// --- Step 7: App calls server tool ---
	demo.Step("App calls server tool via bridge → AppHost → Client").
		Arrow("Bridge", "Host", "SendToHost(tools/call, {name: \"server_echo\"})").
		Arrow("Host", "Client", "Call(tools/call, params)").
		Arrow("Client", "Srv", "JSON-RPC tools/call").
		DashedArrow("Srv", "Client", "ToolResult").
		DashedArrow("Client", "Host", "CallResult").
		DashedArrow("Host", "Bridge", "Response").
		Note("The app calls a server-side tool through the bridge. AppHost forwards to the MCP server via the Client.").
		Run(func() {
			resp, err := bridge.SendToHost(ctx, "tools/call", map[string]any{
				"name":      "server_echo",
				"arguments": map[string]any{"msg": "from the app"},
			})
			if err != nil {
				fmt.Printf("  ERROR: %v\n", err)
				return
			}
			raw, _ := ui.ToBytes(resp.Result)
			var result core.ToolResult
			json.Unmarshal(raw, &result)
			fmt.Printf("  App called server_echo → %s\n", result.Content[0].Text)
		})

	// --- Step 8: Dynamic tool registration ---
	demo.Step("Dynamic registration — app adds a tool at runtime").
		Arrow("Bridge", "Bridge", "RegisterTool(\"app_dice\")").
		Arrow("Bridge", "Host", "notifications/tools/list_changed").
		Arrow("Host", "Bridge", "Send(tools/list) — refresh").
		DashedArrow("Bridge", "Host", "{tools: [app_greet, app_counter, app_dice]}").
		Note("The app registers a new tool after startup. AppHost detects the change and refreshes its cache.").
		Run(func() {
			bridge.RegisterTool("app_dice", core.ToolDef{
				Description: "Roll a random die",
			}, func(args map[string]any) (any, error) {
				return core.ToolResult{
					Content: []core.Content{{Type: "text", Text: "rolled: 4"}},
				}, nil
			})

			// Wait for async refresh.
			time.Sleep(100 * time.Millisecond)

			tools, _ := host.ListAppTools(ctx)
			fmt.Printf("  App tools after dynamic registration (%d):\n", len(tools))
			for _, t := range tools {
				fmt.Printf("    - %s\n", t.Name)
			}

			result, _ := host.CallAppTool(ctx, "app_dice", nil)
			fmt.Printf("  Called app_dice → %s\n", result.Content[0].Text)
		})

	demo.Section("Cleanup",
		"AppHost.Close() closes the bridge. The caller closes the Client separately.",
		"In a real application, you'd defer these in the appropriate scope.",
	)

	demo.Execute()

	// Cleanup.
	if host != nil {
		host.Close()
	}
	if c != nil {
		c.Close()
	}
}
