package main

import (
	"encoding/json"
	"fmt"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
)

// runDemo is the customer-segmentation walkthrough. Acts as a scripted MCP
// host against a server started by `make serve` in another terminal.
//
// Walks five steps:
//
//  1. Connect to the fixture (initialize → session)
//  2. tools/list — verify get-customer-data + its _meta.ui resourceUri
//  3. tools/call get-customer-data {} — full 250-row payload (no filter)
//  4. tools/call get-customer-data {segment: "Enterprise"} — same call
//     shape the iframe's segment dropdown drives
//  5. resources/read on the iframe resourceUri — sanity-check the HTML is
//     served on the wire
//
// Each step attaches a common.WireRecipe (curl + Go) for the wire
// reproduction.
func runDemo() {
	serverURL := common.MCPServerURL()

	demo := demokit.New("customer-segmentation — clustered scatter plot over MCP Apps").
		Dir("customer-segmentation").
		Description("Walks the get-customer-data round trip end-to-end as a scripted MCP client: initialize, tools/list, tools/call with and without a segment filter, and resources/read on the App's iframe HTML. The fixture mirrors upstream's customer-segmentation-server example: 250 customers in 4 segments, drawn from clustered Gaussians (PCG-seeded server-side so the visual baseline is stable). Run `make serve` in another terminal first.").
		Actors(
			demokit.Actor("Host", "MCP Host (this client)"),
			demokit.Actor("Server", "mcpkit-Go fixture (make serve)"),
		)

	demo.Section("Setup",
		"Start the MCP server in a separate terminal first:",
		"",
		"```",
		"Terminal 1:  make serve         # mcpkit-Go fixture on :3101",
		"Terminal 2:  make demo          # this walkthrough (--tui for interactive TUI)",
		"```",
		"",
		"Any MCP host can connect to the running server (Claude Desktop, VS Code, MCPJam, basic-host). The walkthrough below acts as a scripted host that issues the protocol calls directly through `*mcpkit/client.Client` — no LLM, no browser. The same calls drive the iframe when you run `make demo-app EXAMPLE=customer-segmentation` in basic-host (see the [centralized guide](../README.md#other-ways-to-test-a-fixture)).",
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
    core.ClientInfo{Name: "customer-segmentation-host", Version: "1.0"},
)
if err := c.Connect(); err != nil {
    log.Fatalf("connect: %v", err)
}`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		c = client.NewClient(serverURL+"/mcp",
			core.ClientInfo{Name: "customer-segmentation-host", Version: "1.0"},
		)
		if err := c.Connect(); err != nil {
			fmt.Printf("    ERROR: %v\n    Start the server with: make serve\n", err)
			return nil
		}
		fmt.Printf("    connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)
		return nil
	})

	step2 := demo.Step("tools/list — verify get-customer-data + its UI resource").
		Arrow("Host", "Server", "tools/list").
		DashedArrow("Server", "Host", "tools[] including get-customer-data").
		Note("The marker that flags this tool as an App is `_meta.ui.resourceUri` (`ui://customer-segmentation/mcp-app.html`). Hosts that read either the nested (`_meta.ui.resourceUri`) or flat (`_meta.ui/resourceUri`) form both find it. The same tool list is what basic-host renders into its tools sidebar.")
	common.WireRecipe(step2,
		`curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' \
  | jq '.result.tools[] | {name, _meta}'`,
		`res, _ := c.Call("tools/list", map[string]any{})
var out struct {
    Tools []struct {
        Name string         `+"`json:\"name\"`"+`
        Meta map[string]any `+"`json:\"_meta,omitempty\"`"+`
    } `+"`json:\"tools\"`"+`
}
json.Unmarshal(res.Raw, &out)
for _, t := range out.Tools {
    fmt.Printf("  %s: %v\n", t.Name, t.Meta)
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
			fmt.Printf("    ERROR decoding tools/list: %v\n", err)
			return nil
		}
		for _, t := range out.Tools {
			pretty, _ := json.MarshalIndent(t.Meta, "      ", "  ")
			fmt.Printf("    %s\n      _meta: %s\n", t.Name, string(pretty))
		}
		return nil
	})

	step3 := demo.Step("tools/call get-customer-data — full 250-row payload").
		Arrow("Host", "Server", "tools/call get-customer-data {}").
		DashedArrow("Server", "Host", "structuredContent = { customers[250], segments[4] }").
		Note("This is the call the iframe issues on connect — no arguments, server defaults segment to All. The handler memoizes the 250-customer pool behind sync.Once, so subsequent calls reuse the same pool and the scatter plot stays stable as you flip the dropdown.")
	common.WireRecipe(step3,
		`curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get-customer-data","arguments":{}}}' \
  | jq '.result.structuredContent | {n_customers: (.customers | length), segments}'`,
		`tr, _ := c.ToolCallFull("get-customer-data", map[string]any{})
// tr.StructuredContent: { customers: [...], segments: [...] }
sc := tr.StructuredContent.(map[string]any)
fmt.Printf("customers: %d\n", len(sc["customers"].([]any)))
pretty, _ := json.MarshalIndent(sc["segments"], "", "  ")
fmt.Println(string(pretty))`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		tr, err := c.ToolCallFull("get-customer-data", map[string]any{})
		if err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		sc, _ := tr.StructuredContent.(map[string]any)
		if sc == nil {
			fmt.Printf("    structuredContent missing or wrong shape\n")
			return nil
		}
		custs, _ := sc["customers"].([]any)
		segs, _ := sc["segments"].([]any)
		fmt.Printf("    customers: %d, segments: %d\n", len(custs), len(segs))
		if len(custs) > 0 {
			first, _ := json.MarshalIndent(custs[0], "      ", "  ")
			fmt.Printf("    first customer:\n      %s\n", string(first))
		}
		pretty, _ := json.MarshalIndent(segs, "    ", "  ")
		fmt.Printf("    segments: %s\n", string(pretty))
		return nil
	})

	step4 := demo.Step("tools/call get-customer-data {segment: \"Enterprise\"}").
		Arrow("Host", "Server", "tools/call get-customer-data {segment:\"Enterprise\"}").
		DashedArrow("Server", "Host", "structuredContent.customers filtered to Enterprise only").
		Note("This is the exact call the iframe issues when a user picks `Enterprise` from the segment dropdown. The handler filters the cached pool — same `segments[]` summary comes back so the legend can still show the full distribution, but `customers[]` is the Enterprise-only slice. Demonstrates the dropdown round trip without the iframe.")
	common.WireRecipe(step4,
		`curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"get-customer-data","arguments":{"segment":"Enterprise"}}}' \
  | jq '.result.structuredContent.customers | length'`,
		`tr, _ := c.ToolCallFull("get-customer-data", map[string]any{
    "segment": "Enterprise",
})
sc := tr.StructuredContent.(map[string]any)
fmt.Printf("enterprise customers: %d\n", len(sc["customers"].([]any)))`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		tr, err := c.ToolCallFull("get-customer-data", map[string]any{
			"segment": "Enterprise",
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
		custs, _ := sc["customers"].([]any)
		fmt.Printf("    Enterprise-only customers: %d (of 250 total)\n", len(custs))
		if len(custs) > 0 {
			seen := map[string]bool{}
			for _, c := range custs {
				cm, _ := c.(map[string]any)
				if seg, ok := cm["segment"].(string); ok {
					seen[seg] = true
				}
			}
			segs := make([]string, 0, len(seen))
			for s := range seen {
				segs = append(segs, s)
			}
			fmt.Printf("    distinct segment values in response: %v (expect: [Enterprise])\n", segs)
		}
		return nil
	})

	step5 := demo.Step("resources/read on ui://customer-segmentation/mcp-app.html").
		Arrow("Host", "Server", "resources/read { uri: ui://customer-segmentation/mcp-app.html }").
		DashedArrow("Server", "Host", "Contents[0].Text = the iframe HTML").
		Note("Confirms the App's iframe HTML is actually served on the MCP wire. basic-host fetches the same URI once it sees `_meta.ui.resourceUri`. The bytes here should match upstream's `dist/mcp-app.html` build under `EXT_APPS_DIR=/tmp/ext-apps/examples/customer-segmentation-server/dist/`.")
	common.WireRecipe(step5,
		`curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":5,"method":"resources/read","params":{"uri":"ui://customer-segmentation/mcp-app.html"}}' \
  | jq -r '.result.contents[0].text' | head -c 200`,
		`text, _ := c.ReadResource("ui://customer-segmentation/mcp-app.html")
fmt.Printf("first 200 bytes: %.200s\n", text)`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		text, err := c.ReadResource("ui://customer-segmentation/mcp-app.html")
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
		"customer-segmentation's iframe sits at the **moderate** end of the App-ness spectrum — one tool call on load, then a few host-context hooks. The bridge calls its App SDK makes:",
		"",
		"- `app.callServerTool({name: \"get-customer-data\"})` once on connect — fetches the full 250-row pool and seeds Chart.js. The dropdown re-issues the same call with `{segment: ...}`.",
		"- `app.getHostContext()` — reads `theme`, `styles.variables`, `styles.css.fonts`, and `safeAreaInsets` from the host so the chart matches basic-host's light/dark mode and CSS variables.",
		"- `app.onhostcontextchanged` — same handler re-runs when the host flips theme, so Chart.js is destroyed + reinitialised to repaint with the new palette.",
		"",
		"NO `app.registerTool`, NO `app.updateModelContext`, NO `app.ontoolresult` — the iframe drives its own fetch loop instead of waiting for the host to push a tool result. Same shape as `cohort-heatmap`; one rung below the rich `budget-allocator` / `scenario-modeler` patterns that register app-side tools the host can call back into.",
	)

	demo.Section("Where to look in the code",
		"- `main.go` — fixture is ~250 lines: typed tool, paired UI resource, plus the ported clustered-Gaussian generator (`generateCustomers` + `clusteredValue` + Box-Muller). The 250-customer pool is memoized via `sync.Once` so the scatter plot stays stable across calls within a session.",
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
