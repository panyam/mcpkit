package main

import (
	"encoding/json"
	"fmt"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
)

// runDemo is the debug-server walkthrough. Acts as a scripted MCP host
// against a server started by `just serve` in another terminal.
//
// Walks six steps:
//
//  1. Connect to the fixture (initialize → session)
//  2. tools/list — three tools with distinct _meta.ui shapes:
//     debug-tool (model + App), debug-refresh + debug-log (App-only,
//     visibility=["app"]). All three share the same iframe resourceUri.
//  3. tools/call debug-tool {} — default contentType="text", counter
//     starts at 1.
//  4. tools/call debug-tool {largeInput:..., delayMs:..., includeMeta} —
//     exercises the kitchen-sink knobs; counter increments.
//  5. tools/call debug-refresh {} — App-only polling tool that the
//     model normally never sees; the walkthrough exercises it to
//     show the wire shape.
//  6. tools/call debug-log {type:..., payload:...} — App-only logging
//     tool; `payload` is `any` and lands as `{}` on the wire (reflection
//     fix from issue 548).
//
// Each step attaches a common.WireRecipe (curl + Go) for the wire
// reproduction.
func runDemo() {
	serverURL := common.MCPServerURL()

	demo := demokit.New("debug-server — kitchen-sink + App-only polling/logging tools").
		Dir("debug-server").
		Description("Walks the three debug-server tools end-to-end: debug-tool (kitchen-sink: contentType / multipleBlocks / structuredContent / meta / largeInput / simulateError / delayMs knobs), debug-refresh (App-only polling), debug-log (App-only event log). All three share the same iframe resource. First fixture where two tools carry `_meta.ui.visibility=[\"app\"]` AND a shared `_meta.ui.resourceUri` — the iframe owns the resource but exposes app-only knobs to itself via the bridge. Run `just serve` in another terminal first.").
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
		"Any MCP host can connect to the running server. The walkthrough below acts as a scripted host that issues the protocol calls directly through `*mcpkit/client.Client` — no LLM, no browser. The same calls drive the iframe when you run `just demo-app EXAMPLE=debug-server` in basic-host (see the [centralized guide](../README.md#other-ways-to-test-a-fixture)).",
	)

	var c *client.Client

	step1 := demo.Step("Connect to the fixture").
		Arrow("Host", "Server", "POST /mcp — initialize").
		DashedArrow("Server", "Host", "serverInfo + capabilities + Mcp-Session-Id").
		Note("Standard MCP initialize handshake.")
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
    core.ClientInfo{Name: "debug-server-host", Version: "1.0"},
)
if err := c.Connect(); err != nil {
    log.Fatalf("connect: %v", err)
}`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		c = client.NewClient(serverURL+"/mcp",
			core.ClientInfo{Name: "debug-server-host", Version: "1.0"},
		)
		if err := c.Connect(); err != nil {
			fmt.Printf("    ERROR: %v\n    Start the server with: just serve\n", err)
			return nil
		}
		fmt.Printf("    connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)
		return nil
	})

	step2 := demo.Step("tools/list — three tools, two visibility shapes").
		Arrow("Host", "Server", "tools/list").
		DashedArrow("Server", "Host", "tools[] = [debug-tool, debug-refresh, debug-log]").
		Note("Three tools sharing one iframe (`_meta.ui.resourceUri` = `ui://debug-tool/mcp-app.html` on all three). `debug-tool` is model-visible (no visibility array). `debug-refresh` + `debug-log` carry `_meta.ui.visibility = [\"app\"]` — hosts that filter by visibility hide them from the model's planning surface; the iframe sees them and calls them via the bridge.")
	common.WireRecipe(step2,
		`curl -s -X POST `+serverURL+`/mcp -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' \
  | jq '.result.tools[] | {name, ui: ._meta.ui}'`,
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

	step3 := demo.Step("tools/call debug-tool {} — default contentType text").
		Arrow("Host", "Server", "tools/call debug-tool {}").
		DashedArrow("Server", "Host", "structuredContent = { config: {contentType: \"text\"}, timestamp, counter }").
		Note("The kitchen-sink debug tool, called with all-default knobs. `contentType` defaults to `\"text\"`; `counter` is per-process atomic that increments on every call (this walkthrough sees 1 on the first call). `multipleBlocks`, `includeStructuredContent`, `includeMeta` default to true but the framework path that handles them is iframe-side.")
	common.WireRecipe(step3,
		`curl -s -X POST `+serverURL+`/mcp -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"debug-tool","arguments":{}}}' \
  | jq '.result.structuredContent'`,
		`tr, _ := c.ToolCallFull("debug-tool", map[string]any{})
pretty, _ := json.MarshalIndent(tr.StructuredContent, "", "  ")
fmt.Println(string(pretty))`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		tr, err := c.ToolCallFull("debug-tool", map[string]any{})
		if err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		pretty, _ := json.MarshalIndent(tr.StructuredContent, "    ", "  ")
		fmt.Printf("    structuredContent: %s\n", string(pretty))
		return nil
	})

	step4 := demo.Step("tools/call debug-tool with largeInput + delayMs").
		Arrow("Host", "Server", "tools/call debug-tool {largeInput:..., delayMs:200, includeMeta:true}").
		DashedArrow("Server", "Host", "structuredContent.largeInputLength = N; counter increments").
		Note("Exercises the kitchen-sink knobs. `largeInput` is reflected back as `largeInputLength`; counter increments to 2. The other knobs (`multipleBlocks`, `includeStructuredContent`, `includeMeta`, `simulateError`, `contentType` enum branches) flip behavioural shapes on the wire — basic-host's debug panel makes them clickable for manual exploration.")
	common.WireRecipe(step4,
		`curl -s -X POST `+serverURL+`/mcp -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"debug-tool","arguments":{"largeInput":"hello world","delayMs":200,"contentType":"text","includeMeta":true}}}' \
  | jq '.result.structuredContent'`,
		`tr, _ := c.ToolCallFull("debug-tool", map[string]any{
    "largeInput":  "hello world",
    "delayMs":     200,
    "contentType": "text",
    "includeMeta": true,
})
pretty, _ := json.MarshalIndent(tr.StructuredContent, "", "  ")
fmt.Println(string(pretty))`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		tr, err := c.ToolCallFull("debug-tool", map[string]any{
			"largeInput":  "hello world",
			"delayMs":     200,
			"contentType": "text",
			"includeMeta": true,
		})
		if err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		pretty, _ := json.MarshalIndent(tr.StructuredContent, "    ", "  ")
		fmt.Printf("    structuredContent: %s\n", string(pretty))
		return nil
	})

	step5 := demo.Step("tools/call debug-refresh {} — App-only polling tool").
		Arrow("Host", "Server", "tools/call debug-refresh {}").
		DashedArrow("Server", "Host", "structuredContent = { timestamp, counter }").
		Note("The App-only polling tool. The model normally wouldn't call this (it's hidden from visibility-filtered tools/list); the walkthrough invokes it directly to demonstrate the wire shape. `counter` here returns the CURRENT value without incrementing — the iframe uses it to detect whether a fresh debug-tool call landed since the last refresh.")
	common.WireRecipe(step5,
		`curl -s -X POST `+serverURL+`/mcp -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"debug-refresh","arguments":{}}}' \
  | jq '.result.structuredContent'`,
		`tr, _ := c.ToolCallFull("debug-refresh", map[string]any{})
pretty, _ := json.MarshalIndent(tr.StructuredContent, "", "  ")
fmt.Println(string(pretty))`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		tr, err := c.ToolCallFull("debug-refresh", map[string]any{})
		if err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		pretty, _ := json.MarshalIndent(tr.StructuredContent, "    ", "  ")
		fmt.Printf("    structuredContent: %s\n", string(pretty))
		return nil
	})

	step6 := demo.Step("tools/call debug-log {type, payload} — App-only event log").
		Arrow("Host", "Server", "tools/call debug-log {type:\"click\", payload:{...}}").
		DashedArrow("Server", "Host", "structuredContent = { logged: true, logFile }").
		Note("The App-only log tool. `payload` is `any` in Go and reflects to `{}` on the wire (the schema fix from issue 548 Gap 2 lets `any` land as the same shape upstream's `z.unknown()` produces — no `InputSchemaOverride` needed). Server emits a fixed log path; production code would write into `logFile` for off-iframe inspection.")
	common.WireRecipe(step6,
		`curl -s -X POST `+serverURL+`/mcp -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"debug-log","arguments":{"type":"click","payload":{"buttonId":"refresh","timestamp":"2026-06-08T00:00:00Z"}}}}' \
  | jq '.result.structuredContent'`,
		`tr, _ := c.ToolCallFull("debug-log", map[string]any{
    "type":    "click",
    "payload": map[string]any{"buttonId": "refresh", "timestamp": "2026-06-08T00:00:00Z"},
})
pretty, _ := json.MarshalIndent(tr.StructuredContent, "", "  ")
fmt.Println(string(pretty))`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		tr, err := c.ToolCallFull("debug-log", map[string]any{
			"type":    "click",
			"payload": map[string]any{"buttonId": "refresh", "timestamp": "2026-06-08T00:00:00Z"},
		})
		if err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		pretty, _ := json.MarshalIndent(tr.StructuredContent, "    ", "  ")
		fmt.Printf("    structuredContent: %s\n", string(pretty))
		return nil
	})

	demo.Section("What the App iframe does with all this",
		"The host → iframe handoff mechanics (`tools/call` → `resources/read` → sandboxed iframe → `postMessage`) are covered once in [The basic-host bridge dance](../README.md#the-basic-host-bridge-dance). Steps 1-6 above are the floor every MCP Apps interaction starts from.",
		"",
		"debug-server's iframe sits at the **rich** end of the App-ness spectrum — three tools sharing one iframe, two of them App-only. The bridge calls its App SDK makes:",
		"",
		"- `app.ontoolresult` for `debug-tool` — renders each variation the model picks (text / image / audio / resource / resourceLink / mixed content blocks; isError simulation; delay).",
		"- `app.callServerTool({name: \"debug-refresh\"})` on a polling cadence — checks for new debug-tool calls since the last refresh. Same App-only-tool pattern as `system-monitor`'s `poll-system-stats`.",
		"- `app.callServerTool({name: \"debug-log\", arguments: {type, payload}})` on iframe interactions — writes events to the shared log file for off-iframe inspection.",
		"- `app.getHostContext()` + `app.onhostcontextchanged` — applies host theme to the debug panel.",
		"",
		"NO `app.registerTool`, NO `app.updateModelContext`. This is the most-tool-heavy fixture short of `pdf-server` and `wiki-explorer`.",
	)

	demo.Section("Where to look in the code",
		"- `main.go` — fixture is ~200 lines. Three interesting bits: (1) `debug-tool` uses `ui.RegisterTypedAppTool` with the kitchen-sink input/output struct; (2) `debug-refresh` + `debug-log` drop down to `core.TypedTool` + `core.WithToolMeta(&core.ToolMeta{UI: {ResourceUri, Visibility: [\"app\"]}})` to land App-only tools that share the same iframe (the `RegisterTypedAppTool` helper would double-register the UI resource); (3) `debug-log`'s `Payload any` field reflects to `{}` thanks to the issue 548 Gap 2 schema fix — same shape upstream's `z.unknown()` produces, no `InputSchemaOverride` needed.",
		"- `walkthrough.go` — this file. Each step's curl + Go recipe is the canonical wire reproduction.",
		"- `../README.md` — narrative + screenshots + the centralized [Other ways to test a fixture](../README.md#other-ways-to-test-a-fixture) guide and the bridge-dance reference section.",
	)

	_ = step1
	_ = step2
	_ = step3
	_ = step4
	_ = step5
	_ = step6

	common.SetupRenderer(demo)
	demo.Execute()
}
