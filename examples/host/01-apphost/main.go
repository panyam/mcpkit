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
	"log"
	"time"

	"github.com/panyam/demokit"
	"github.com/panyam/demokit/notebookbridge"
	"github.com/panyam/demokit/tui"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
	commonotel "github.com/panyam/mcpkit/examples/common/otel"
	"github.com/panyam/mcpkit/examples/host/refs"
	ui "github.com/panyam/mcpkit/ext/ui"
	"github.com/panyam/mcpkit/server"
)

func main() {
	// SEP-414 P6 (issue 660): wire telemetry on both the client side
	// (matches PR 689's pattern across other walkthroughs) AND the
	// AppHost forward path (new in this PR). Default --exporter=""
	// keeps every path on Noop — zero overhead for the typical demo
	// run; pass --exporter=otlp to ship traces to the LGTM stack.
	tel := common.ExporterFromArgs()
	tp, shutdown, err := commonotel.SetupClientTelemetry(context.Background(),
		commonotel.WithExporter(*tel.Exporter),
		commonotel.WithOTLPEndpoint(*tel.OTLPEndpoint),
		commonotel.WithServiceName("apphost-demo-host"),
	)
	if err != nil {
		log.Fatalf("commonotel.SetupClientTelemetry: %v", err)
	}
	defer shutdown(context.Background())

	demo := demokit.New("AppHost and host-side app management").
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
		srv    *server.Server
		c      *client.Client
		bridge *ui.InProcessAppBridge
		host   *ui.AppHost
		ctx    = context.Background()

		// What a real host would hand its model loop: the app's latest
		// context slot and any follow-up turns the app asked for.
		modelContext ui.ModelContextUpdate
		pendingTurns []ui.MessageRequest
	)

	// --- Step 1: Create MCP server ---
	demo.Step("Create MCP server with tools").
		Ref(refs.MCPSpec).
		Arrow("Srv", "Srv", "RegisterTool(\"server_echo\")").
		Arrow("Srv", "Srv", "RegisterTool(\"server_time\")").
		Note("The server provides two tools: echo (returns input) and time (returns current time).").
		Run(func(_ demokit.StepContext) *demokit.StepResult {
			srv = server.NewServer(core.ServerInfo{Name: "demo-server", Version: "1.0"})

			type echoInput struct {
				Msg string `json:"msg,omitempty" jsonschema:"description=Message to echo back"`
			}
			srv.Register(core.TextTool[echoInput]("server_echo", "Echo back the input",
				func(ctx core.ToolContext, input echoInput) (string, error) {
					return "echo: " + input.Msg, nil
				},
			))
			srv.Register(core.TextTool[struct{}]("server_time", "Get current time",
				func(ctx core.ToolContext, _ struct{}) (string, error) {
					return time.Now().Format(time.RFC3339), nil
				},
			))
			fmt.Println("  Server created with 2 tools: server_echo, server_time")
			return nil
		})

	// --- Step 2: Connect client ---
	demo.Step("Connect client to server via in-process transport").
		Arrow("Client", "Srv", "initialize").
		DashedArrow("Srv", "Client", "capabilities, serverInfo").
		Note("The client connects without HTTP, using InProcessTransport for direct dispatch.").
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			xport := server.NewInProcessTransport(srv)
			c = client.NewClient("memory://", core.ClientInfo{Name: "demo-host", Version: "1.0"},
				client.WithTracerProvider(tp),
				client.WithTransport(xport),
				client.WithUIExtension(),
			)
			if err := c.Connect(ctx.Ctx); err != nil {
				fmt.Printf("  ERROR: %v\n", err)
				return nil
			}
			fmt.Printf("  Connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)

			tools, _ := c.ListTools(ctx.Ctx)
			fmt.Printf("  Server tools: ")
			for i, t := range tools {
				if i > 0 {
					fmt.Print(", ")
				}
				fmt.Print(t.Name)
			}
			fmt.Println()
			return nil
		})

	// --- Step 3: Create app bridge with tools ---
	demo.Step("Create InProcessAppBridge with app-provided tools").
		Ref(refs.MCPAppsSpec).
		Arrow("Bridge", "Bridge", "RegisterTool(\"app_greet\")").
		Arrow("Bridge", "Bridge", "RegisterTool(\"app_counter\")").
		Note("The bridge simulates an MCP App (iframe). It registers two tools that the host/model can call directly.").
		Run(func(_ demokit.StepContext) *demokit.StepResult {
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
			return nil
		})

	// --- Step 4: Create and start AppHost ---
	demo.Step("Create AppHost and wire everything together").
		Arrow("Host", "Host", "WithHostHandlers (ui/* host capabilities)").
		Arrow("Host", "Bridge", "SetRequestHandler (app→host)").
		Arrow("Host", "Bridge", "SetNotificationHandler (list_changed)").
		Arrow("Host", "Bridge", "Start()").
		Arrow("Host", "Bridge", "Send(tools/list), initial fetch").
		DashedArrow("Bridge", "Host", "{tools: [app_greet, app_counter]}").
		Note("AppHost wires up bidirectional routing and fetches the initial app tool list. HostHandlers supplies the ui/* methods that belong to the host, not the server.").
		Run(func(_ demokit.StepContext) *demokit.StepResult {
			handlers := ui.HostHandlers{
				UpdateModelContext: func(_ context.Context, u ui.ModelContextUpdate) error {
					modelContext = u // one slot per view: each update replaces the last
					return nil
				},
				Message: func(_ context.Context, m ui.MessageRequest) error {
					pendingTurns = append(pendingTurns, m)
					return nil
				},
				OpenLink: func(_ context.Context, r ui.OpenLinkRequest) error {
					fmt.Printf("  [host] would open %s in the user's browser\n", r.URL)
					return nil
				},
				RequestDisplayMode: func(_ context.Context, r ui.DisplayModeRequest) (ui.DisplayModeResult, error) {
					if r.Mode == "pip" {
						return ui.DisplayModeResult{Mode: "inline"}, nil // this host has no pip
					}
					return ui.DisplayModeResult{Mode: r.Mode}, nil
				},
			}
			host = ui.NewAppHost(c, bridge, ui.WithTracerProvider(tp), ui.WithHostHandlers(handlers))
			if err := host.Start(ctx); err != nil {
				fmt.Printf("  ERROR: %v\n", err)
				return nil
			}
			fmt.Println("  AppHost started: bridge handlers wired, initial tool list fetched")
			return nil
		})

	// --- Step 5: List all tools ---
	demo.Step("ListAllTools, the aggregated server + app tools").
		Arrow("Host", "Client", "ListTools(), server tools").
		DashedArrow("Client", "Host", "[server_echo, server_time]").
		Arrow("Host", "Bridge", "cached app tools").
		DashedArrow("Bridge", "Host", "[app_greet, app_counter]").
		Note("ListAllTools merges tools from the MCP server and the app bridge into a single list.").
		Run(func(_ demokit.StepContext) *demokit.StepResult {
			tools, err := host.ListAllTools(ctx)
			if err != nil {
				fmt.Printf("  ERROR: %v\n", err)
				return nil
			}
			fmt.Printf("  All tools (%d total):\n", len(tools))
			for _, t := range tools {
				fmt.Printf("    - %s (%s)\n", t.Name, t.Description)
			}
			return nil
		})

	// --- Step 6: Call app tool ---
	demo.Step("CallAppTool, where the host invokes an app-provided tool").
		Arrow("Host", "Bridge", "Send(tools/call, {name: \"app_greet\", args: {name: \"World\"}})").
		DashedArrow("Bridge", "Host", "ToolResult {text: \"Hello, World!\"}").
		Note("The host calls a tool registered by the app. The bridge dispatches to the Go handler.").
		Run(func(_ demokit.StepContext) *demokit.StepResult {
			result, err := host.CallAppTool(ctx, "app_greet", map[string]any{"name": "World"})
			if err != nil {
				fmt.Printf("  ERROR: %v\n", err)
				return nil
			}
			fmt.Printf("  Result: %s\n", result.Content[0].Text)

			// Call counter twice to show state.
			host.CallAppTool(ctx, "app_counter", nil)
			result2, _ := host.CallAppTool(ctx, "app_counter", nil)
			fmt.Printf("  Counter after 2 calls: %s\n", result2.Content[0].Text)
			return nil
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
		Run(func(_ demokit.StepContext) *demokit.StepResult {
			resp, err := bridge.SendToHost(ctx, "tools/call", map[string]any{
				"name":      "server_echo",
				"arguments": map[string]any{"msg": "from the app"},
			})
			if err != nil {
				fmt.Printf("  ERROR: %v\n", err)
				return nil
			}
			raw, _ := ui.ToBytes(resp.Result)
			var result core.ToolResult
			json.Unmarshal(raw, &result)
			fmt.Printf("  App called server_echo → %s\n", result.Content[0].Text)
			return nil
		})

	// --- Step 8: App calls the host ---
	demo.Step("App calls the host, not the server").
		Ref(refs.MCPAppsSpec).
		Arrow("Bridge", "Host", "ui/update-model-context {content, structuredContent}").
		DashedArrow("Host", "Bridge", "{}").
		Arrow("Bridge", "Host", "ui/message {role: user, content}").
		DashedArrow("Host", "Bridge", "{}").
		Arrow("Bridge", "Host", "ui/open-link, ui/request-display-mode").
		Note("Four of the app's requests are host capabilities the MCP server has never heard of. AppHost answers them from HostHandlers and forwards only tools/call and resources/read.").
		Run(func(_ demokit.StepContext) *demokit.StepResult {
			for _, city := range []string{"Detroit", "Ann Arbor"} {
				bridge.SendToHost(ctx, ui.MethodUpdateModelContext, map[string]any{
					"content":           []map[string]any{{"type": "text", "text": "Map is centred on " + city}},
					"structuredContent": map[string]any{"city": city},
				})
			}
			fmt.Printf("  Model context slot: %q (the Detroit update was replaced)\n", modelContext.Content[0].Text)

			bridge.SendToHost(ctx, ui.MethodMessage, map[string]any{
				"role": "user", "content": map[string]any{"type": "text", "text": "What's near here?"},
			})
			fmt.Printf("  Follow-up turns queued for the model: %d (%q)\n", len(pendingTurns), pendingTurns[0].Content[0].Text)

			bridge.SendToHost(ctx, ui.MethodOpenLink, map[string]any{"url": "https://example.com/detroit"})

			resp, _ := bridge.SendToHost(ctx, ui.MethodRequestDisplayMode, map[string]any{"mode": "pip"})
			raw, _ := ui.ToBytes(resp.Result)
			fmt.Printf("  Asked for pip, host granted %s\n", raw)
			return nil
		})

	// --- Step 9: Dynamic tool registration ---
	demo.Step("Dynamic registration, where the app adds a tool at runtime").
		Arrow("Bridge", "Bridge", "RegisterTool(\"app_dice\")").
		Arrow("Bridge", "Host", "notifications/tools/list_changed").
		Arrow("Host", "Bridge", "Send(tools/list), refresh").
		DashedArrow("Bridge", "Host", "{tools: [app_greet, app_counter, app_dice]}").
		Note("The app registers a new tool after startup. AppHost detects the change and refreshes its cache.").
		Run(func(_ demokit.StepContext) *demokit.StepResult {
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
			return nil
		})

	demo.Section("Cleanup",
		"AppHost.Close() closes the bridge. The caller closes the Client separately.",
		"In a real application, you'd defer these in the appropriate scope.",
	)

	// --mode=plain (default) | tui | notebook. --tui / --note are honored
	// as aliases for --mode=tui / --mode=notebook.
	switch demokit.Mode() {
	case "tui":
		demo.WithRenderer(tui.New())
	case "notebook":
		demo.WithRenderer(notebookbridge.New())
	}

	demo.Execute()

	// Cleanup.
	if host != nil {
		host.Close()
	}
	if c != nil {
		c.Close()
	}
}
