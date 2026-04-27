// 02-multi-server demonstrates ServerRegistry — managing multiple MCP
// servers with unified tool aggregation, routing, and collision resolution.
//
// Everything runs in-process: 3 real MCP servers, each with different tools
// (some overlapping), connected via InProcessTransport. A ServerRegistry
// aggregates them and routes tool calls based on name or explicit server ID.
package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/host/refs"
	ui "github.com/panyam/mcpkit/ext/ui"
	"github.com/panyam/mcpkit/server"
)

func main() {
	demo := demokit.New("Multi-Server Registry").
		Dir("02-multi-server").
		RunPrefix("examples/host").
		Description("Demonstrates ServerRegistry managing 3 MCP servers with tool aggregation, collision resolution, and app bridge integration.").
		Actors(
			demokit.Actor("Reg", "ServerRegistry"),
			demokit.Actor("W", "Weather Server"),
			demokit.Actor("C", "Calendar Server"),
			demokit.Actor("K", "Clock Server"),
			demokit.Actor("Br", "App Bridge"),
		)

	var (
		reg                                  *ui.ServerRegistry
		weatherClient, calendarClient, clockClient *client.Client
		ctx                                  = context.Background()
	)

	// Helper to create a server + connected client.
	newServer := func(name string, tools map[string]string) *client.Client {
		srv := server.NewServer(core.ServerInfo{Name: name, Version: "1.0"})
		for tName, desc := range tools {
			n := tName
			srv.RegisterTool(
				core.ToolDef{Name: n, Description: desc, InputSchema: map[string]any{"type": "object"}},
				func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
					return core.TextResult(fmt.Sprintf("[%s] %s: ok", name, n)), nil
				},
			)
		}
		xport := server.NewInProcessTransport(srv)
		c := client.NewClient("memory://", core.ClientInfo{Name: "registry-demo", Version: "1.0"},
			client.WithTransport(xport),
		)
		c.Connect()
		return c
	}

	// --- Step 1: Create servers ---
	demo.Step("Create 3 MCP servers with overlapping tools").
		Ref(refs.MCPSpec).
		Arrow("W", "W", "get_forecast, get_alerts, get_info").
		Arrow("C", "C", "list_events, create_event, get_info").
		Arrow("K", "K", "get_time").
		Note("Weather and Calendar both have a 'get_info' tool — this will cause a collision in the registry.").
		Run(func() {
			weatherClient = newServer("weather", map[string]string{
				"get_forecast": "Get weather forecast",
				"get_alerts":   "Get weather alerts",
				"get_info":     "Get weather service info",
			})
			calendarClient = newServer("calendar", map[string]string{
				"list_events":  "List calendar events",
				"create_event": "Create a calendar event",
				"get_info":     "Get calendar service info",
			})
			clockClient = newServer("clock", map[string]string{
				"get_time": "Get current time",
			})
			fmt.Println("  Created 3 servers: weather (3 tools), calendar (3 tools), clock (1 tool)")
			fmt.Println("  Note: 'get_info' exists in both weather and calendar")
		})

	// --- Step 2: Create registry with resolver ---
	demo.Step("Create ServerRegistry with ToolResolver and CollisionHandler").
		Ref(refs.MCPKitDocs).
		Arrow("Reg", "Reg", "WithToolResolver(arg-based routing)").
		Arrow("Reg", "Reg", "WithCollisionHandler(log collisions)").
		Note("The resolver picks a server based on args. The collision handler logs when ambiguity is detected.").
		Run(func() {
			reg = ui.NewServerRegistry(
				ui.WithToolResolver(func(ctx context.Context, name string, candidates []ui.RegisteredTool, args map[string]any) (string, error) {
					// Route based on a "source" hint in the args.
					if src, ok := args["source"].(string); ok {
						for _, c := range candidates {
							if c.ServerID == src {
								return src, nil
							}
						}
					}
					// Default: pick the first candidate.
					fmt.Printf("    Resolver: no hint, defaulting to %s\n", candidates[0].ServerID)
					return candidates[0].ServerID, nil
				}),
				ui.WithCollisionHandler(func(toolName string, serverIDs []string) {
					fmt.Printf("    ⚠ Collision detected: '%s' in servers [%s]\n", toolName, strings.Join(serverIDs, ", "))
				}),
			)
			fmt.Println("  Registry created with arg-based resolver and collision logger")
		})

	// --- Step 3: Add servers ---
	demo.Step("Add all 3 servers — collision detected").
		Arrow("Reg", "W", "Add(\"weather\", weatherClient)").
		Arrow("Reg", "C", "Add(\"calendar\", calendarClient)").
		Arrow("Reg", "K", "Add(\"clock\", clockClient)").
		Note("When calendar is added, the registry detects that 'get_info' now exists in both weather and calendar.").
		Run(func() {
			reg.Add(ctx, "weather", weatherClient)
			fmt.Println("  Added weather")
			reg.Add(ctx, "calendar", calendarClient)
			fmt.Println("  Added calendar")
			reg.Add(ctx, "clock", clockClient)
			fmt.Println("  Added clock")
			fmt.Printf("  Servers: %v\n", reg.Servers())
		})

	// --- Step 4: AllTools ---
	demo.Step("AllTools — aggregated tool list with routing metadata").
		Arrow("Reg", "Reg", "AllTools()").
		Note("Returns all tools from all servers. Each tool has clean name + ServerID metadata.").
		Run(func() {
			tools, _ := reg.AllTools(ctx)
			fmt.Printf("  %d tools across 3 servers:\n", len(tools))
			for _, t := range tools {
				fmt.Printf("    %-15s  server=%-10s  %s\n", t.Name, t.ServerID, t.Description)
			}
		})

	// --- Step 5: Unambiguous call ---
	demo.Step("CallTool (unambiguous) — routes directly").
		Arrow("Reg", "W", "CallTool(\"get_forecast\")").
		DashedArrow("W", "Reg", "ToolResult").
		Note("get_forecast exists only in weather — no resolver needed, routes directly.").
		Run(func() {
			result, err := reg.CallTool(ctx, "get_forecast", nil)
			if err != nil {
				fmt.Printf("  ERROR: %v\n", err)
				return
			}
			fmt.Printf("  Result: %s\n", result.Content[0].Text)
		})

	// --- Step 6: Ambiguous call ---
	demo.Step("CallTool (ambiguous) — resolver invoked").
		Arrow("Reg", "Reg", "CallTool(\"get_info\", {source: \"calendar\"})").
		Arrow("Reg", "Reg", "Resolver picks calendar").
		Arrow("Reg", "C", "tools/call").
		DashedArrow("C", "Reg", "ToolResult").
		Note("get_info is ambiguous (weather + calendar). The resolver sees {source: \"calendar\"} and picks calendar.").
		Run(func() {
			result, err := reg.CallTool(ctx, "get_info", map[string]any{"source": "calendar"})
			if err != nil {
				fmt.Printf("  ERROR: %v\n", err)
				return
			}
			fmt.Printf("  Result: %s\n", result.Content[0].Text)
		})

	// --- Step 7: Explicit routing ---
	demo.Step("CallToolOn — explicit routing bypasses resolver").
		Arrow("Reg", "W", "CallToolOn(\"weather\", \"get_info\")").
		DashedArrow("W", "Reg", "ToolResult").
		Note("CallToolOn routes directly to the specified server. No resolver involved.").
		Run(func() {
			result, err := reg.CallToolOn(ctx, "weather", "get_info", nil)
			if err != nil {
				fmt.Printf("  ERROR: %v\n", err)
				return
			}
			fmt.Printf("  Result: %s\n", result.Content[0].Text)
		})

	// --- Step 8: Remove server ---
	demo.Step("Remove server — tools disappear from index").
		Arrow("Reg", "K", "Remove(\"clock\")").
		Note("After removing clock, get_time is no longer available.").
		Run(func() {
			reg.Remove("clock")
			fmt.Printf("  Remaining servers: %v\n", reg.Servers())

			_, err := reg.CallTool(ctx, "get_time", nil)
			if err != nil {
				fmt.Printf("  CallTool(\"get_time\"): %v ✓\n", err)
			}
		})

	// --- Step 9: Add with app bridge ---
	demo.Step("AddWithBridge — server with app-provided tools").
		Ref(refs.MCPAppsSpec).
		Arrow("Reg", "K", "AddWithBridge(\"clock-v2\", client, bridge)").
		Arrow("Br", "Br", "RegisterTool(\"app_stopwatch\")").
		Note("Re-adds clock with an app bridge that provides an extra tool. Both server and app tools appear in AllTools.").
		Run(func() {
			// Recreate clock server.
			clockClient = newServer("clock-v2", map[string]string{
				"get_time": "Get current time (v2)",
			})

			bridge := ui.NewInProcessAppBridge()
			bridge.RegisterTool("app_stopwatch", core.ToolDef{
				Description: "Start/stop a stopwatch",
			}, func(args map[string]any) (any, error) {
				return core.ToolResult{
					Content: []core.Content{{Type: "text", Text: "stopwatch: 00:00:05"}},
				}, nil
			})

			reg.AddWithBridge(ctx, "clock-v2", clockClient, bridge)
			fmt.Printf("  Servers: %v\n", reg.Servers())

			tools, _ := reg.AllTools(ctx)
			fmt.Printf("  All tools (%d):\n", len(tools))
			for _, t := range tools {
				fmt.Printf("    %-15s  server=%-10s  source=%-6s  %s\n", t.Name, t.ServerID, t.Source, t.Description)
			}

			// Call the app tool through the registry.
			result, err := reg.CallTool(ctx, "app_stopwatch", nil)
			if err != nil {
				fmt.Printf("  ERROR: %v\n", err)
				return
			}
			fmt.Printf("  Called app_stopwatch → %s\n", result.Content[0].Text)
		})

	demo.Execute()

	// Cleanup.
	if reg != nil {
		reg.Close()
	}
	if weatherClient != nil {
		weatherClient.Close()
	}
	if calendarClient != nil {
		calendarClient.Close()
	}
	if clockClient != nil {
		clockClient.Close()
	}
}
