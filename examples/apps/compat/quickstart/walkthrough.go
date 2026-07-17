package main

import (
	"encoding/json"
	"fmt"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
)

// runDemo is the quickstart walkthrough. Acts as a scripted MCP host
// against a server started by `just serve` in another terminal.
//
// Walks four steps + a closing narrative Section. The wire surface is
// the same minimum-viable shape as basic-vanillajs (one tool, one ui://
// resource) but text-content-only — quickstart's get-time returns the
// timestamp as a plain text content block, no structuredContent. The
// pedagogical contrast is on the app-side dance: the quickstart iframe's
// bridge usage is the BARE MINIMUM (ontoolresult + callServerTool, no
// app.registerTool, no app.updateModelContext), which makes this the
// cleanest fixture for understanding what an MCP App actually IS at
// the bridge level.
//
//   1. Connect (initialize + notifications/initialized).
//   2. tools/list — focus on get-time's `_meta.ui.resourceUri`, the
//      signal that says "this tool has an iframe to render."
//   3. tools/call get-time — capture the text content (ISO 8601
//      timestamp). No structuredContent here; the iframe reads the
//      text directly from content[0].text.
//   4. resources/read on ui://get-time/mcp-app.html — pull the iframe
//      bytes plus inspect the per-content `_meta` payload.
//   5. (Section) "What the App iframe does with all this" — narrates
//      quickstart's minimum-viable bridge dance: app.ontoolresult
//      renders the time into the DOM, then a button calls
//      app.callServerTool({name: "get-time"}) to fetch a fresh
//      timestamp via the bridge — the simplest possible
//      iframe-calls-back-to-server pattern. Contrast with
//      budget-allocator's five app.registerTool calls + updateModelContext;
//      quickstart shows the floor of what an MCP App can be.
//
// Each tool/resource step attaches two unboxed Verbatim blocks via
// common.WireRecipe: a curl form (copy-pastable) and a Go form (the
// equivalent *client.Client call).
func runDemo() {
	serverURL := common.MCPServerURL()

	demo := demokit.New("quickstart — minimum-viable MCP App with the bare-minimum bridge dance").
		Dir("quickstart").
		Description("Walks the get-time round trip end-to-end as a scripted MCP client. quickstart is the simplest fixture in apps/compat — one tool, one resource, text content only — but its iframe also uses the bare-minimum bridge surface (ontoolresult + callServerTool), making it the cleanest fixture for understanding what an MCP App fundamentally IS. The final narrative section spells out the bridge dance a Go-only host can't directly drive. Run `just serve` in another terminal first.").
		Actors(
			demokit.Actor("Host", "MCP Host (this client)"),
			demokit.Actor("Server", "mcpkit-Go fixture (just serve)"),
		)

	demo.Section("Setup",
		"Start the MCP server in a separate terminal first:",
		"",
		"```",
		"Terminal 1:  just serve         # mcpkit-Go fixture on :3101 by default",
		"Terminal 2:  just demo          # this walkthrough (--tui for interactive TUI)",
		"```",
		"",
		"Point the walkthrough at a different host via either of:",
		"",
		"```",
		"go run . --demo --url http://localhost:1234       # CLI flag",
		"MCPKIT_SERVER_URL=http://localhost:1234 just demo # env var",
		"```",
		"",
		"Any MCP host can connect to the running server (Claude Desktop, VS Code, MCPJam, basic-host). The walkthrough below acts as a scripted host that issues the protocol calls directly through `*mcpkit/client.Client` — no LLM, no browser, no JS. The same protocol calls drive the iframe when you run `just demo-app EXAMPLE=quickstart` in basic-host (see the [centralized guide](../README.md#other-ways-to-test-a-fixture)).",
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
    core.ClientInfo{Name: "quickstart-host", Version: "1.0"},
)
if err := c.Connect(); err != nil {
    log.Fatalf("connect: %v", err)
}
// Connect() handles initialize + notifications/initialized + session header.
fmt.Printf("connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		c = client.NewClient(serverURL+"/mcp",
			core.ClientInfo{Name: "quickstart-host", Version: "1.0"},
		)
		if err := c.Connect(); err != nil {
			fmt.Printf("    ERROR: %v\n    Start the server with: just serve\n", err)
			return nil
		}
		fmt.Printf("    connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)
		return nil
	})

	step2 := demo.Step("tools/list — verify get-time + its UI resource").
		Arrow("Host", "Server", "tools/list").
		DashedArrow("Server", "Host", "tools[] including get-time").
		Note("`get-time` is the single server-side tool. The pedagogically interesting bit isn't the tool name — it's `_meta.ui.resourceUri` (`ui://get-time/mcp-app.html`). That URI is the signal to an MCP Apps host: \"render this tool's result inside the iframe served from that resource.\" A non-Apps host would just see a JSON tool result; an Apps host wires it into a sandboxed iframe.")
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

	step3 := demo.Step("tools/call get-time — the timestamp the iframe renders").
		Arrow("Host", "Server", "tools/call get-time {}").
		DashedArrow("Server", "Host", "ToolResult.content[0].text = \"2026-…Z\"").
		Note("The handler returns a single text content block — the current server time formatted as ISO 8601. No structuredContent on this fixture; the iframe reads the timestamp directly from `result.content[0].text`. This same content shape is what the iframe receives via `app.ontoolresult`; see the final section.")
	common.WireRecipe(step3,
		`curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get-time","arguments":{}}}' \
  | jq '.result.content[0]'`,
		`tr, _ := c.ToolCallFull("get-time", map[string]any{})
// tr.Content[0].Text is the ISO 8601 timestamp; no structuredContent here.
fmt.Printf("text content: %s\n", tr.Content[0].Text)`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		tr, err := c.ToolCallFull("get-time", map[string]any{})
		if err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		if len(tr.Content) > 0 {
			fmt.Printf("    content[0]: type=%q text=%q\n", tr.Content[0].Type, tr.Content[0].Text)
		}
		if tr.StructuredContent != nil {
			fmt.Printf("    structuredContent: %v  (unexpected for this fixture)\n", tr.StructuredContent)
		} else {
			fmt.Printf("    structuredContent: <none>  (quickstart returns text-only)\n")
		}
		return nil
	})

	step4 := demo.Step("resources/read — the iframe HTML the App ships in").
		Arrow("Host", "Server", "resources/read { uri: ui://get-time/mcp-app.html }").
		DashedArrow("Server", "Host", "Contents[0].text + Contents[0]._meta").
		Note("Pulls the iframe HTML the App loads inside. The walkthrough reports just the byte count (the body is upstream's verbatim bundled JS+HTML; not interesting to dump on the wire) plus whatever `_meta` the resource carries. quickstart's `_meta` is bare — compare to sheet-music's `_meta.ui.csp.connectDomains` and transcript's `_meta.ui.permissions.microphone` to see how richer fixtures use the per-content `_meta` slot.")
	common.WireRecipe(step4,
		`curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":4,"method":"resources/read","params":{"uri":"ui://get-time/mcp-app.html"}}' \
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
    "uri": "ui://get-time/mcp-app.html",
})
json.Unmarshal(res.Raw, &raw)
fmt.Printf("mimeType: %s\n", raw.Contents[0].MimeType)
fmt.Printf("bytes:    %d\n", len(raw.Contents[0].Text))
fmt.Printf("_meta:    %s\n", string(raw.Contents[0].Meta))`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		res, err := c.Call("resources/read", map[string]any{
			"uri": "ui://get-time/mcp-app.html",
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
		"quickstart's iframe sits at the **bare-minimum** end of the App-ness spectrum — the cleanest reference for what differentiates an MCP App from \"just an MCP server with a UI hint.\" The bridge calls its App SDK makes:",
		"",
		"- `app.ontoolresult` — extracts `result.content[0].text` and writes it into the DOM as \"Server Time: <ISO timestamp>\".",
		"- `app.callServerTool({name: \"get-time\"})` on button click — the iframe calling *back* to the server to refresh. The simplest possible iframe-talks-to-server pattern.",
		"",
		"That's it. NO `app.registerTool` (no model-callable tools), NO `app.updateModelContext` (no model-context pushback), no `getHostContext` theming. Compare with `cohort-heatmap` (adds host context + filter-driven re-fetches) and `budget-allocator` (full rich dance: 5 registered tools + model context updates).",
	)

	demo.Section("Where to look in the code",
		"- `main.go` — the fixture is ~60 lines: one TypedAppTool returning a text content block, one paired UI resource, CORS for browser hosts.",
		"- `walkthrough.go` — this file. Each step's curl + Go recipe is the canonical wire reproduction.",
		"- `../README.md` — narrative + screenshots + the [Other ways to test a fixture](../README.md#other-ways-to-test-a-fixture) guide.",
		"- Upstream's iframe source: `/tmp/ext-apps/examples/quickstart/src/mcp-app.ts` — 26 lines of JS that show the minimum-viable bridge dance.",
	)

	_ = step1
	_ = step2
	_ = step3
	_ = step4

	common.SetupRenderer(demo)
	demo.Execute()
}
