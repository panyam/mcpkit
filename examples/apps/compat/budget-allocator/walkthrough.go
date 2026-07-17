package main

import (
	"encoding/json"
	"fmt"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
)

// runDemo is the budget-allocator walkthrough. Acts as a scripted MCP host
// against a server started by `just serve` in another terminal.
//
// Walks four steps, each anchored on a fixture-specific surface — and
// ends with a narrative Section painting the *app-side* picture that
// a Go-only scripted host can't directly execute (because it has no
// JS runtime and no basic-host bridge):
//
//  1. Connect (initialize + notifications/initialized).
//  2. tools/list — focus on get-budget-data's `_meta.ui.resourceUri`,
//     which is what tells an MCP Apps host "this tool has an iframe to
//     render."
//  3. tools/call get-budget-data — capture the structuredContent and
//     summarize its shape: nested Config (categories + presets) +
//     Analytics (24 months of history + benchmarks by stage). The
//     wire-level fact is that this rich nested payload reflects cleanly
//     from Go structs + maps without any schema overrides — the whole
//     reason this fixture exists.
//  4. resources/read on ui://budget-allocator/mcp-app.html — pull the
//     iframe HTML (just report the byte count) plus inspect the
//     per-content `_meta` payload. budget-allocator's _meta is currently
//     bare; the contrast with sheet-music's CSP block and transcript's
//     permissions block makes the per-fixture `_meta` story visible.
//  5. (Section) "What the App iframe does with all this" — narrates the
//     app-side dance that takes over from here when this fixture is
//     loaded inside an MCP Apps host like basic-host: the iframe's JS
//     receives the tool result via `app.ontoolresult`, registers FIVE
//     app-side tools the model can call through the bridge
//     (`get-allocations`, `set-allocation`, `set-total-budget`,
//     `set-company-stage`, `get-benchmark-comparison`), and uses
//     `app.updateModelContext` to push UI state back to the model. The
//     scripted walkthrough can't run JS, but a reader walking step-by-
//     step learns where the server-side story ends and the app-side
//     story begins.
//
// Each tool/resource step attaches two unboxed Verbatim blocks via
// common.WireRecipe: a curl form (copy-pastable) and a Go form (the
// equivalent *client.Client call).
func runDemo() {
	serverURL := common.MCPServerURL()

	demo := demokit.New("budget-allocator — deeply nested SaaS budget data + 5 app-side tools").
		Dir("budget-allocator").
		Description("Walks the get-budget-data round trip end-to-end as a scripted MCP client. Two distinctive things about this fixture are visible on the wire: the deeply nested structuredContent payload (config + analytics) that the Go reflector emits cleanly from struct tags + maps, and the iframe HTML that hosts the App. The final narrative section paints the app-side picture — five tools the iframe registers via the bridge — that a Go-only host can't directly drive. Run `just serve` in another terminal first.").
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
		"Any MCP host can connect to the running server (Claude Desktop, VS Code, MCPJam, basic-host). The walkthrough below acts as a scripted host that issues the protocol calls directly through `*mcpkit/client.Client` — no LLM, no browser, no JS. The same protocol calls drive the iframe when you run `just demo-app EXAMPLE=budget-allocator` in basic-host (see the [centralized guide](../README.md#other-ways-to-test-a-fixture)).",
	)

	var c *client.Client

	step1 := demo.Step("Connect to the fixture").
		Arrow("Host", "Server", "POST /mcp — initialize").
		DashedArrow("Server", "Host", "serverInfo + capabilities + Mcp-Session-Id").
		Note("Session establishment is a single initialize round-trip plus the notifications/initialized ack. `*client.Client.Connect()` does both behind one call; the curl form below shows the raw shape.")
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
    core.ClientInfo{Name: "budget-allocator-host", Version: "1.0"},
)
if err := c.Connect(); err != nil {
    log.Fatalf("connect: %v", err)
}
// Connect() handles initialize + notifications/initialized + session header.
fmt.Printf("connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		c = client.NewClient(serverURL+"/mcp",
			core.ClientInfo{Name: "budget-allocator-host", Version: "1.0"},
		)
		if err := c.Connect(); err != nil {
			fmt.Printf("    ERROR: %v\n    Start the server with: just serve\n", err)
			return nil
		}
		fmt.Printf("    connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)
		return nil
	})

	step2 := demo.Step("tools/list — verify get-budget-data + its UI resource").
		Arrow("Host", "Server", "tools/list").
		DashedArrow("Server", "Host", "tools[] including get-budget-data").
		Note("`get-budget-data` is the single server-side tool. The pedagogically interesting bit isn't the tool name — it's `_meta.ui.resourceUri` (`ui://budget-allocator/mcp-app.html`). That URI is the signal to an MCP Apps host: \"render this tool's result inside the iframe served from that resource.\" A non-Apps host would just see a JSON tool result; an Apps host wires it into a sandboxed iframe.")
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

	step3 := demo.Step("tools/call get-budget-data — the rich nested payload").
		Arrow("Host", "Server", "tools/call get-budget-data {}").
		DashedArrow("Server", "Host", "ToolResult.structuredContent = { config: {...}, analytics: {...} }").
		Note("The handler returns a typed `budgetDataOutput` struct with deeply nested fields: Config (categories + preset budgets + currency) and Analytics (24 months of historical allocations + per-stage benchmark bands). The framework marshals it into `structuredContent` automatically via reflection — no `OutputSchemaOverride`, no `OutputSchemaPatch`. The walkthrough prints just the field tree below; the full payload is large enough that dumping it would drown the wire detail. This same payload is what the iframe receives via `app.ontoolresult`; see the final section.")
	common.WireRecipe(step3,
		`curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get-budget-data","arguments":{}}}' \
  | jq '.result.structuredContent | { config_keys: (.config | keys), analytics_keys: (.analytics | keys), category_count: (.config.categories | length), history_months: (.analytics.history | length), benchmark_stages: (.analytics.benchmarks | length) }'`,
		`tr, _ := c.ToolCallFull("get-budget-data", map[string]any{})
// tr.StructuredContent is map[string]any with deeply nested config + analytics.
sc := tr.StructuredContent.(map[string]any)
cfg := sc["config"].(map[string]any)
an := sc["analytics"].(map[string]any)
fmt.Printf("categories:       %d\n", len(cfg["categories"].([]any)))
fmt.Printf("history months:   %d\n", len(an["history"].([]any)))
fmt.Printf("benchmark stages: %d\n", len(an["benchmarks"].([]any)))`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		tr, err := c.ToolCallFull("get-budget-data", map[string]any{})
		if err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		sc, _ := tr.StructuredContent.(map[string]any)
		if sc == nil {
			fmt.Printf("    ERROR: no structuredContent on tool result\n")
			return nil
		}
		cfg, _ := sc["config"].(map[string]any)
		an, _ := sc["analytics"].(map[string]any)
		fmt.Printf("    structuredContent shape:\n")
		fmt.Printf("      config.categories       = %d entries\n", lengthOf(cfg, "categories"))
		fmt.Printf("      config.presetBudgets    = %d entries\n", lengthOf(cfg, "presetBudgets"))
		if v, ok := cfg["defaultBudget"]; ok {
			fmt.Printf("      config.defaultBudget    = %v\n", v)
		}
		if v, ok := cfg["currency"]; ok {
			fmt.Printf("      config.currency         = %v\n", v)
		}
		fmt.Printf("      analytics.history       = %d months\n", lengthOf(an, "history"))
		fmt.Printf("      analytics.benchmarks    = %d stages\n", lengthOf(an, "benchmarks"))
		fmt.Printf("      analytics.stages        = %d names\n", lengthOf(an, "stages"))
		if v, ok := an["defaultStage"]; ok {
			fmt.Printf("      analytics.defaultStage  = %v\n", v)
		}
		return nil
	})

	step4 := demo.Step("resources/read — the iframe HTML the App ships in").
		Arrow("Host", "Server", "resources/read { uri: ui://budget-allocator/mcp-app.html }").
		DashedArrow("Server", "Host", "Contents[0].text + Contents[0]._meta").
		Note("Pulls the iframe HTML the App loads inside. The walkthrough reports just the byte count (the body is upstream's verbatim bundled JS+HTML; not interesting to dump on the wire) plus whatever `_meta` the resource carries. budget-allocator's `_meta` is currently bare — compare to sheet-music's `_meta.ui.csp.connectDomains` (soundfont CDN) and transcript's `_meta.ui.permissions.microphone` for the per-fixture _meta story.")
	common.WireRecipe(step4,
		`curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":4,"method":"resources/read","params":{"uri":"ui://budget-allocator/mcp-app.html"}}' \
  | jq '.result.contents[0] | { mimeType, bytes: (.text | length), _meta }'`,
		`var raw struct {
    Contents []struct {
        URI      string          `+"`json:\"uri\"`"+`
        MimeType string          `+"`json:\"mimeType\"`"+`
        Text     string          `+"`json:\"text\"`"+`
        Meta     json.RawMessage `+"`json:\"_meta\"`"+`
    } `+"`json:\"contents\"`"+`
}
res, _ := c.Call("resources/read", map[string]any{
    "uri": "ui://budget-allocator/mcp-app.html",
})
json.Unmarshal(res.Raw, &raw)
fmt.Printf("mimeType: %s\n", raw.Contents[0].MimeType)
fmt.Printf("bytes:    %d\n", len(raw.Contents[0].Text))
fmt.Printf("_meta:    %s\n", string(raw.Contents[0].Meta))`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		res, err := c.Call("resources/read", map[string]any{
			"uri": "ui://budget-allocator/mcp-app.html",
		})
		if err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		var raw struct {
			Contents []struct {
				URI      string          `json:"uri"`
				MimeType string          `json:"mimeType"`
				Text     string          `json:"text"`
				Meta     json.RawMessage `json:"_meta"`
			} `json:"contents"`
		}
		if err := json.Unmarshal(res.Raw, &raw); err != nil {
			fmt.Printf("    ERROR decoding resources/read: %v\n", err)
			return nil
		}
		if len(raw.Contents) == 0 {
			fmt.Printf("    no contents returned\n")
			return nil
		}
		c0 := raw.Contents[0]
		fmt.Printf("    uri:      %s\n", c0.URI)
		fmt.Printf("    mimeType: %s\n", c0.MimeType)
		fmt.Printf("    bytes:    %d  (iframe HTML+JS bundle)\n", len(c0.Text))
		if len(c0.Meta) > 0 {
			pretty, _ := json.MarshalIndent(json.RawMessage(c0.Meta), "      ", "  ")
			fmt.Printf("    _meta:\n      %s\n", string(pretty))
		} else {
			fmt.Printf("    _meta:    <none>  (no csp / permissions declared)\n")
		}
		return nil
	})

	demo.Section("What the App iframe does with all this",
		"The host → iframe handoff mechanics (`tools/call` → `resources/read` → sandboxed iframe → `postMessage`) are covered once in [The basic-host bridge dance](../README.md#the-basic-host-bridge-dance). Steps 1-4 above are the floor every MCP Apps interaction starts from.",
		"",
		"budget-allocator's iframe sits at the **rich** end of the App-ness spectrum. The bridge calls its App SDK makes:",
		"",
		"- `app.ontoolresult` — unpacks `result.structuredContent` into the typed `BudgetDataResponse` shape; categories / sliders / chart are bound directly to that data.",
		"- `app.registerTool` ×5 — registers **five app-side tools** the model can call back through the bridge: `get-allocations`, `set-allocation`, `set-total-budget`, `set-company-stage`, `get-benchmark-comparison`. None of these are visible from a direct server-side `tools/list` (you can confirm by re-reading step 2's output) — they live in the iframe's runtime and the bridge proxies them to the host.",
		"- `app.updateModelContext` after every UI interaction — pushes the current allocation state back to the model, so a next-turn LLM prompt sees the updated budget.",
		"- `app.getHostContext` + `app.onhostcontextchanged` — pulls host theming so the App chrome adapts.",
		"",
		"This is what makes Budget Allocator a *widget the model can drive* rather than just a JSON tool result. Compare with `quickstart` (bare-minimum bridge dance) and `cohort-heatmap` (moderate — `callServerTool` re-fetches but no model-callable tools).",
	)

	demo.Section("Where to look in the code",
		"- `main.go` — the fixture is one TypedAppTool (~100 lines plus the LCG'd historical data). The whole nested output reflects from the `budgetDataOutput` struct + maps.",
		"- `walkthrough.go` — this file. Each step's curl + Go recipe is the canonical wire reproduction.",
		"- `../README.md` — narrative + screenshots + the [Other ways to test a fixture](../README.md#other-ways-to-test-a-fixture) guide.",
		"- Upstream's iframe source: `/tmp/ext-apps/examples/budget-allocator-server/src/mcp-app.ts` — the JS that consumes `ontoolresult` and registers the five app-side tools. Worth a read to see the bridge dance end-to-end.",
	)

	_ = step1
	_ = step2
	_ = step3
	_ = step4

	common.SetupRenderer(demo)
	demo.Execute()
}

// lengthOf is a small helper for printing the size of a nested array field
// without panicking on missing keys / wrong types. Returns 0 when the key
// is absent or the value isn't a JSON array.
func lengthOf(m map[string]any, key string) int {
	if v, ok := m[key].([]any); ok {
		return len(v)
	}
	return 0
}
