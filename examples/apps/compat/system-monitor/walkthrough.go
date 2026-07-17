package main

import (
	"encoding/json"
	"fmt"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
)

// runDemo is the system-monitor walkthrough. Acts as a scripted MCP host
// against a server started by `just serve` in another terminal.
//
// Walks five steps:
//
//  1. Connect to the fixture (initialize → session)
//  2. tools/list — verify both tools side-by-side: get-system-info
//     carries _meta.ui.resourceUri (App tool), poll-system-stats has
//     _meta.ui.visibility=["app"] (App-only — hidden from the model)
//     with NO resourceUri. Demonstrates the App-only-tool framework
//     knob.
//  3. tools/call get-system-info {} — static hostname / platform /
//     CPU / memory snapshot the iframe seeds from on first load.
//  4. tools/call poll-system-stats {} — dynamic per-core CPU + memory
//     + uptime sample the iframe polls every second for live charts.
//  5. resources/read on ui://system-monitor/mcp-app.html — the iframe.
//
// Each step attaches a common.WireRecipe (curl + Go) for the wire
// reproduction.
func runDemo() {
	serverURL := common.MCPServerURL()

	demo := demokit.New("system-monitor — App-only polling tool + visibility flag").
		Dir("system-monitor").
		Description("Walks the get-system-info + poll-system-stats round trip end-to-end as a scripted MCP client. Two tools side-by-side: one is a model-visible App tool, the other is App-only (visible to the iframe but hidden from model `tools/list` when filtered by visibility). First example of the `_meta.ui.visibility` framework knob on the wire. Run `just serve` in another terminal first.").
		Actors(
			demokit.Actor("Host", "MCP Host (this client)"),
			demokit.Actor("Server", "mcpkit-Go fixture (just serve)"),
		)

	demo.Section("Setup",
		"Start the MCP server in a separate terminal first:",
		"",
		"```",
		"Terminal 1:  just serve         # mcpkit-Go fixture on :3101",
		"Terminal 2:  just demo          # this walkthrough (--tui for interactive TUI)",
		"```",
		"",
		"Any MCP host can connect to the running server. The walkthrough below acts as a scripted host that issues the protocol calls directly through `*mcpkit/client.Client` — no LLM, no browser. The same calls drive the iframe when you run `just demo-app EXAMPLE=system-monitor` in basic-host (see the [centralized guide](../README.md#other-ways-to-test-a-fixture)).",
	)

	var c *client.Client

	step1 := demo.Step("Connect to the fixture").
		Arrow("Host", "Server", "POST /mcp — initialize").
		DashedArrow("Server", "Host", "serverInfo + capabilities + Mcp-Session-Id").
		Note("Standard MCP initialize handshake. `*client.Client.Connect()` runs initialize + notifications/initialized + stashes the session header for every subsequent call.")
	common.WireRecipe(step1,
		`SID=$(curl -si -X POST `+serverURL+`/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"curl","version":"1"}}}' \
  | awk 'tolower($1) == "mcp-session-id:" {gsub(/\r/,""); print $2}')

curl -s -X POST `+serverURL+`/mcp -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized"}'`,
		`c := client.NewClient("`+serverURL+`/mcp",
    core.ClientInfo{Name: "system-monitor-host", Version: "1.0"},
)
if err := c.Connect(); err != nil {
    log.Fatalf("connect: %v", err)
}`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		c = client.NewClient(serverURL+"/mcp",
			core.ClientInfo{Name: "system-monitor-host", Version: "1.0"},
		)
		if err := c.Connect(); err != nil {
			fmt.Printf("    ERROR: %v\n    Start the server with: just serve\n", err)
			return nil
		}
		fmt.Printf("    connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)
		return nil
	})

	step2 := demo.Step("tools/list — App tool + App-only tool side-by-side").
		Arrow("Host", "Server", "tools/list").
		DashedArrow("Server", "Host", "tools[] = [get-system-info, poll-system-stats]").
		Note("Two distinct shapes side-by-side. `get-system-info` is a standard App tool — `_meta.ui.resourceUri` points at the iframe HTML, no visibility filter (model + App both see it). `poll-system-stats` carries `_meta.ui.visibility = [\"app\"]` and NO `resourceUri` — it's still on tools/list (the App registers it as a server tool it can call via the bridge) but a host that filters by visibility would hide it from the model's planning surface. mcpkit lands the App-only shape via `core.WithToolMeta(&core.ToolMeta{UI: {Visibility: [\"app\"]}})` on the typed handler since the `ui.RegisterTypedAppTool` helper insists on a paired UI resource.")
	common.WireRecipe(step2,
		`curl -s -X POST `+serverURL+`/mcp -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' \
  | jq '.result.tools[] | {name, ui_meta: ._meta.ui}'`,
		`res, _ := c.Call("tools/list", map[string]any{})
var out struct {
    Tools []struct {
        Name string         `+"`json:\"name\"`"+`
        Meta map[string]any `+"`json:\"_meta,omitempty\"`"+`
    } `+"`json:\"tools\"`"+`
}
json.Unmarshal(res.Raw, &out)
for _, t := range out.Tools {
    fmt.Printf("  %s  ui=%v\n", t.Name, t.Meta["ui"])
}`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		res, err := c.Call("tools/list", map[string]any{})
		if err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		var out struct {
			Tools []struct {
				Name string         `json:"name"`
				Meta map[string]any `json:"_meta,omitempty"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(res.Raw, &out); err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		for _, t := range out.Tools {
			meta, _ := json.MarshalIndent(t.Meta, "      ", "  ")
			fmt.Printf("    %s\n      _meta: %s\n", t.Name, string(meta))
		}
		return nil
	})

	step3 := demo.Step("tools/call get-system-info {} — static system info").
		Arrow("Host", "Server", "tools/call get-system-info {}").
		DashedArrow("Server", "Host", "structuredContent = { hostname, platform, arch, cpu, memory }").
		Note("Static information the iframe seeds its header / metadata panel with on first load. Hostname comes from `os.Hostname()`; platform / arch from `runtime.GOOS` / `runtime.GOARCH`; CPU count from `runtime.NumCPU()`. Total memory is a placeholder — visual test doesn't depend on its accuracy, and Go has no stdlib hook for it without cgo.")
	common.WireRecipe(step3,
		`curl -s -X POST `+serverURL+`/mcp -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get-system-info","arguments":{}}}' \
  | jq '.result.structuredContent'`,
		`tr, _ := c.ToolCallFull("get-system-info", map[string]any{})
pretty, _ := json.MarshalIndent(tr.StructuredContent, "", "  ")
fmt.Println(string(pretty))`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		tr, err := c.ToolCallFull("get-system-info", map[string]any{})
		if err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		pretty, _ := json.MarshalIndent(tr.StructuredContent, "    ", "  ")
		fmt.Printf("    structuredContent: %s\n", string(pretty))
		return nil
	})

	step4 := demo.Step("tools/call poll-system-stats {} — dynamic sample").
		Arrow("Host", "Server", "tools/call poll-system-stats {}").
		DashedArrow("Server", "Host", "structuredContent = { cpu.cores[N], memory, uptime, timestamp }").
		Note("Dynamic metrics the iframe polls every ~1s to drive the live CPU + memory charts. `cpu.cores[]` is one entry per logical core (`{idle, total}` timer ticks — upstream's shape from Node's `os.cpus()`). mcpkit ships a stub shape (right length, zero values) since Go's stdlib doesn't expose per-core CPU time without cgo. Visual test masks the charts; real consumers can swap in a `gopsutil` backend if they need real numbers.")
	common.WireRecipe(step4,
		`curl -s -X POST `+serverURL+`/mcp -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"poll-system-stats","arguments":{}}}' \
  | jq '.result.structuredContent | {core_count: (.cpu.cores | length), memory, uptime, timestamp}'`,
		`tr, _ := c.ToolCallFull("poll-system-stats", map[string]any{})
sc := tr.StructuredContent.(map[string]any)
cores := sc["cpu"].(map[string]any)["cores"].([]any)
fmt.Printf("cores: %d, memory: %v, uptime: %v, timestamp: %v\n",
    len(cores), sc["memory"], sc["uptime"], sc["timestamp"])`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		tr, err := c.ToolCallFull("poll-system-stats", map[string]any{})
		if err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		sc, _ := tr.StructuredContent.(map[string]any)
		if sc == nil {
			fmt.Printf("    structuredContent missing\n")
			return nil
		}
		cores := 0
		if cpu, ok := sc["cpu"].(map[string]any); ok {
			if cs, ok := cpu["cores"].([]any); ok {
				cores = len(cs)
			}
		}
		fmt.Printf("    cores: %d\n", cores)
		fmt.Printf("    memory: %v\n", sc["memory"])
		fmt.Printf("    uptime: %v\n", sc["uptime"])
		fmt.Printf("    timestamp: %v\n", sc["timestamp"])
		return nil
	})

	step5 := demo.Step("resources/read on ui://system-monitor/mcp-app.html").
		Arrow("Host", "Server", "resources/read { uri: ui://system-monitor/mcp-app.html }").
		DashedArrow("Server", "Host", "Contents[0].Text = the iframe HTML").
		Note("Sanity-check the iframe HTML is on the wire. No CSP — the iframe runs the charts entirely in JavaScript and never talks to anything outside the MCP bridge.")
	common.WireRecipe(step5,
		`curl -s -X POST `+serverURL+`/mcp -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":5,"method":"resources/read","params":{"uri":"ui://system-monitor/mcp-app.html"}}' \
  | jq -r '.result.contents[0].text' | head -c 200`,
		`text, _ := c.ReadResource("ui://system-monitor/mcp-app.html")
fmt.Printf("%d bytes; first 200:\n%.200s\n", len(text), text)`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		text, err := c.ReadResource("ui://system-monitor/mcp-app.html")
		if err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		preview := text
		if len(preview) > 200 {
			preview = preview[:200] + "…"
		}
		fmt.Printf("    %d bytes; first 200:\n      %s\n", len(text), preview)
		return nil
	})

	demo.Section("What the App iframe does with all this",
		"The host → iframe handoff mechanics (`tools/call` → `resources/read` → sandboxed iframe → `postMessage`) are covered once in [The basic-host bridge dance](../README.md#the-basic-host-bridge-dance). Steps 1-5 above are the floor every MCP Apps interaction starts from.",
		"",
		"system-monitor's iframe sits at the **moderate** end of the App-ness spectrum. The bridge calls its App SDK makes:",
		"",
		"- `app.ontoolresult` for `get-system-info` — seeds the metadata header on first connect.",
		"- `app.callServerTool({name: \"poll-system-stats\"})` every ~1s — drives the live CPU + memory charts. This is the App-only tool from step 2; the model never sees it.",
		"- `app.getHostContext()` + `app.onhostcontextchanged` — applies host theme to the charts.",
		"",
		"NO `app.registerTool`, NO `app.updateModelContext`. Same spectrum rung as `customer-segmentation` / `cohort-heatmap` / `scenario-modeler`, with the additional twist of the App-only polling tool.",
	)

	demo.Section("Where to look in the code",
		"- `main.go` — fixture is ~210 lines. The interesting bit: tool 2 (`poll-system-stats`) drops out of `ui.RegisterTypedAppTool` (which insists on a paired UI resource) into `core.TypedTool` + `srv.RegisterTool` with `core.WithToolMeta(&core.ToolMeta{UI: {Visibility: [\"app\"]}})`. Documents the framework gap: ext/ui could expose a \"typed tool with metadata only\" helper for this shape.",
		"- `walkthrough.go` — this file. Each step's curl + Go recipe is the canonical wire reproduction.",
		"- `../README.md` — narrative + screenshots + the centralized [Other ways to test a fixture](../README.md#other-ways-to-test-a-fixture) guide and the bridge-dance reference section.",
	)

	_ = step1
	_ = step2
	_ = step3
	_ = step4
	_ = step5

	common.SetupRenderer(demo)
	demo.Execute()
}
