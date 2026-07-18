package main

import (
	"encoding/json"
	"fmt"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
)

// runDemo is the scenario-modeler walkthrough. Acts as a scripted MCP host
// against a server started by `just serve` in another terminal.
//
// Walks five steps:
//
//  1. Connect to the fixture (initialize → session)
//  2. tools/list — verify get-scenario-data + its _meta.ui resourceUri,
//     and call out the `execution.taskSupport: "forbidden"` declaration
//     (the tool is sync-only — it can't be wrapped in an MCP Task)
//  3. tools/call get-scenario-data {} — 5 pre-built templates + defaults
//  4. tools/call get-scenario-data {customInputs: ...} — server-side
//     12-month projection + summary for a one-off scenario
//  5. resources/read on the iframe resourceUri — sanity-check the HTML
//     is served on the wire
//
// Each step attaches a common.WireRecipe (curl + Go) for the wire
// reproduction.
func runDemo() {
	serverURL := common.MCPServerURL()

	demo := demokit.New("scenario-modeler — SaaS projection templates over MCP Apps").
		Dir("scenario-modeler").
		Description("Walks the get-scenario-data round trip end-to-end as a scripted MCP client: initialize, tools/list, two tools/call shapes (default + customInputs), and resources/read on the App's iframe HTML. The fixture mirrors upstream's scenario-modeler-server example: 5 pre-built SaaS scenarios (Bootstrapped, VC Rocketship, Cash Cow, Turnaround, Efficient Growth) each with 12 months of MRR / gross profit / net profit projections plus a summary card. Run `just serve` in another terminal first.").
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
		"Any MCP host can connect to the running server (Claude Desktop, VS Code, MCPJam, basic-host). The walkthrough below acts as a scripted host that issues the protocol calls directly through `*mcpkit/client.Client` — no LLM, no browser. The same calls drive the iframe when you run `just demo-app EXAMPLE=scenario-modeler` in basic-host (see the [centralized guide](../README.md#other-ways-to-test-a-fixture)).",
	)

	var c *client.Client

	step1 := demo.Step("Connect to the fixture").
		Arrow("Host", "Server", "POST /mcp — initialize").
		DashedArrow("Server", "Host", "serverInfo + capabilities + Mcp-Session-Id").
		Note("Standard MCP initialize handshake. `*client.Client.Connect()` runs initialize + notifications/initialized + stashes the session header for every subsequent call.")
	common.WireRecipe(step1,
		`# Initialize. Mcp-Session-Id comes back in the response headers.
SID=$(curl -si -X POST `+serverURL+`/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"curl","version":"1"}}}' \
  | awk 'tolower($1) == "mcp-session-id:" {gsub(/\r/,""); print $2}')

# Acknowledge so the server's dispatcher unblocks.
curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized"}'`,
		`c := client.NewClient("`+serverURL+`/mcp",
    core.ClientInfo{Name: "scenario-modeler-host", Version: "1.0"},
)
if err := c.Connect(); err != nil {
    log.Fatalf("connect: %v", err)
}`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		c = client.NewClient(serverURL+"/mcp",
			core.ClientInfo{Name: "scenario-modeler-host", Version: "1.0"},
		)
		if err := c.Connect(); err != nil {
			fmt.Printf("    ERROR: %v\n    Start the server with: just serve\n", err)
			return nil
		}
		fmt.Printf("    connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)
		return nil
	})

	step2 := demo.Step("tools/list — verify get-scenario-data + _meta surfaces").
		Arrow("Host", "Server", "tools/list").
		DashedArrow("Server", "Host", "tools[] including get-scenario-data").
		Note("Two _meta-adjacent surfaces worth calling out here. (1) `_meta.ui.resourceUri` (`ui://scenario-modeler/mcp-app.html`) tags this tool as an MCP App — basic-host fetches that URI to fill the iframe. (2) `execution.taskSupport: \"forbidden\"` declares this tool is sync-only — it cannot be wrapped in an MCP Task. mcpkit-Go emits this explicitly via `core.ToolExecution{TaskSupport: core.TaskSupportForbidden}`; upstream's TS server doesn't surface the same knob, so this is a mcpkit-side declaration that's spec-compliant on the wire.")
	common.WireRecipe(step2,
		`curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' \
  | jq '.result.tools[] | {name, execution, _meta}'`,
		`res, _ := c.Call("tools/list", map[string]any{})
var out struct {
    Tools []struct {
        Name      string         `+"`json:\"name\"`"+`
        Execution map[string]any `+"`json:\"execution,omitempty\"`"+`
        Meta      map[string]any `+"`json:\"_meta,omitempty\"`"+`
    } `+"`json:\"tools\"`"+`
}
json.Unmarshal(res.Raw, &out)
for _, t := range out.Tools {
    fmt.Printf("  %s  execution=%v  _meta=%v\n", t.Name, t.Execution, t.Meta)
}`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		res, err := c.Call("tools/list", map[string]any{})
		if err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		var out struct {
			Tools []struct {
				Name      string         `json:"name"`
				Execution map[string]any `json:"execution,omitempty"`
				Meta      map[string]any `json:"_meta,omitempty"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(res.Raw, &out); err != nil {
			fmt.Printf("    ERROR decoding tools/list: %v\n", err)
			return nil
		}
		for _, t := range out.Tools {
			exec, _ := json.MarshalIndent(t.Execution, "      ", "  ")
			meta, _ := json.MarshalIndent(t.Meta, "      ", "  ")
			fmt.Printf("    %s\n      execution: %s\n      _meta: %s\n", t.Name, string(exec), string(meta))
		}
		return nil
	})

	step3 := demo.Step("tools/call get-scenario-data — 5 templates + defaults").
		Arrow("Host", "Server", "tools/call get-scenario-data {}").
		DashedArrow("Server", "Host", "structuredContent = { templates[5], defaultInputs }").
		Note("This is the call the iframe issues on connect — no arguments, server returns the 5 pre-built scenarios (Bootstrapped, VC Rocketship, Cash Cow, Turnaround, Efficient Growth) plus the slider defaults. Each template has its 12-month MRR projection + summary card pre-computed server-side, so the iframe's `Compare to...` dropdown can render the comparison overlay without any extra round trip.")
	common.WireRecipe(step3,
		`curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get-scenario-data","arguments":{}}}' \
  | jq '.result.structuredContent | {n_templates: (.templates|length), templateNames: [.templates[]|{id,icon,name}], defaultInputs}'`,
		`tr, _ := c.ToolCallFull("get-scenario-data", map[string]any{})
sc := tr.StructuredContent.(map[string]any)
tpls := sc["templates"].([]any)
fmt.Printf("templates: %d\n", len(tpls))
pretty, _ := json.MarshalIndent(sc["defaultInputs"], "", "  ")
fmt.Printf("defaultInputs: %s\n", string(pretty))`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		tr, err := c.ToolCallFull("get-scenario-data", map[string]any{})
		if err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		sc, _ := tr.StructuredContent.(map[string]any)
		if sc == nil {
			fmt.Printf("    structuredContent missing\n")
			return nil
		}
		tpls, _ := sc["templates"].([]any)
		fmt.Printf("    templates: %d\n", len(tpls))
		for _, t := range tpls {
			tm, _ := t.(map[string]any)
			fmt.Printf("      %s %s — %s\n", tm["icon"], tm["name"], tm["keyInsight"])
		}
		pretty, _ := json.MarshalIndent(sc["defaultInputs"], "    ", "  ")
		fmt.Printf("    defaultInputs: %s\n", string(pretty))
		return nil
	})

	step4 := demo.Step("tools/call get-scenario-data {customInputs: ...} — one-off projection").
		Arrow("Host", "Server", "tools/call get-scenario-data {customInputs:{...}}").
		DashedArrow("Server", "Host", "structuredContent.customSummary + customProjections[12]").
		Note("Optional path — the iframe can also POST a one-off scenario for server-side computation. Send the same 5 parameters the sliders model and the server returns 12 monthly projections plus a summary card (MRR, ARR, total revenue, total profit, growth %, avg margin, break-even month — `null` if profit never crosses zero). With the defaults this lands at ~$63.4K end MRR / $684K total revenue / $187K total profit — same numbers the iframe renders in its `Your Scenario` panel.")
	common.WireRecipe(step4,
		`curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"get-scenario-data","arguments":{"customInputs":{"startingMRR":50000,"monthlyGrowthRate":5,"monthlyChurnRate":3,"grossMargin":80,"fixedCosts":30000}}}}' \
  | jq '.result.structuredContent.customSummary'`,
		`tr, _ := c.ToolCallFull("get-scenario-data", map[string]any{
    "customInputs": map[string]any{
        "startingMRR":       50000,
        "monthlyGrowthRate": 5,
        "monthlyChurnRate":  3,
        "grossMargin":       80,
        "fixedCosts":        30000,
    },
})
sc := tr.StructuredContent.(map[string]any)
sum, _ := json.MarshalIndent(sc["customSummary"], "", "  ")
fmt.Printf("customSummary: %s\n", string(sum))`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		tr, err := c.ToolCallFull("get-scenario-data", map[string]any{
			"customInputs": map[string]any{
				"startingMRR":       50000,
				"monthlyGrowthRate": 5,
				"monthlyChurnRate":  3,
				"grossMargin":       80,
				"fixedCosts":        30000,
			},
		})
		if err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		sc, _ := tr.StructuredContent.(map[string]any)
		if sc == nil {
			fmt.Printf("    structuredContent missing\n")
			return nil
		}
		sum, _ := json.MarshalIndent(sc["customSummary"], "    ", "  ")
		fmt.Printf("    customSummary: %s\n", string(sum))
		if projs, ok := sc["customProjections"].([]any); ok {
			fmt.Printf("    customProjections: %d months\n", len(projs))
		}
		return nil
	})

	step5 := demo.Step("resources/read on ui://scenario-modeler/mcp-app.html").
		Arrow("Host", "Server", "resources/read { uri: ui://scenario-modeler/mcp-app.html }").
		DashedArrow("Server", "Host", "Contents[0].Text = the iframe HTML").
		Note("Confirms the App's iframe HTML is actually served on the MCP wire. basic-host fetches the same URI once it sees `_meta.ui.resourceUri`. The bytes here should match upstream's `dist/mcp-app.html` build under `EXT_APPS_DIR=/tmp/ext-apps/examples/scenario-modeler-server/dist/`.")
	common.WireRecipe(step5,
		`curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":5,"method":"resources/read","params":{"uri":"ui://scenario-modeler/mcp-app.html"}}' \
  | jq -r '.result.contents[0].text' | head -c 200`,
		`text, _ := c.ReadResource("ui://scenario-modeler/mcp-app.html")
fmt.Printf("first 200 bytes: %.200s\n", text)`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		text, err := c.ReadResource("ui://scenario-modeler/mcp-app.html")
		if err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		preview := text
		if len(preview) > 200 {
			preview = preview[:200] + "…"
		}
		fmt.Printf("    %d bytes total; first 200:\n      %s\n", len(text), preview)
		return nil
	})

	demo.Section("What the App iframe does with all this",
		"The host → iframe handoff mechanics (`tools/call` → `resources/read` → sandboxed iframe → `postMessage`) are covered once in [The basic-host bridge dance](../README.md#the-basic-host-bridge-dance). Steps 1-5 above are the floor every MCP Apps interaction starts from.",
		"",
		"scenario-modeler's iframe sits at the **moderate** end of the App-ness spectrum — one tool call on load, no app-side tools registered. The bridge calls its App SDK makes:",
		"",
		"- `app.ontoolresult` — unpacks `result.structuredContent` into `{templates, defaultInputs}` and seeds React state. The `Compare to...` dropdown reads from `templates[]`; the `Reset` button reads from `defaultInputs`.",
		"- `app.getHostContext()` + `app.onhostcontextchanged` — reads `safeAreaInsets` from the host and applies them as padding on the root `<main>` element, so the layout respects the host's chrome / status-bar safe area.",
		"",
		"NO `app.registerTool`, NO `app.updateModelContext`, NO `app.callServerTool` after the initial load — slider changes are computed entirely client-side using the same projection math that the server exposes. Server-side projection (Step 4) is offered for hosts that want to drive the comparison from outside the iframe; the iframe itself doesn't use it. Same spectrum rung as `customer-segmentation` and `cohort-heatmap`.",
	)

	demo.Section("Where to look in the code",
		"- `main.go` — fixture is ~370 lines. The interesting parts: ported `calculateProjections` + `calculateSummary` (`math.Pow` for compounded MRR, nullable `*float64` for `breakEvenMonth`), the 5 `buildTemplate(...)` calls that materialize `scenarioTemplates` at startup, and the `OutputSchemaOverride` block that hand-stitches the nullable `breakEvenMonth` `anyOf` shape at both template-summary and customSummary depths.",
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
