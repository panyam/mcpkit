package main

import (
	"encoding/json"
	"fmt"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
)

// runDemo is the integration walkthrough. Acts as a scripted MCP host
// against a server started by `make serve` in another terminal.
//
// Walks five steps:
//
//  1. Connect to the fixture (initialize → session)
//  2. tools/list — get-time + resources/list — sample-report.txt
//     (the downloadable text resource demonstrating ResourceLink)
//  3. tools/call get-time {} — RFC 3339 nano timestamp
//  4. resources/read on resource:///sample-report.txt — verify
//     each read gives a fresh "Generated: <now>" line (the host's
//     ui/download-file path serves this through ResourceLink)
//  5. resources/read on the iframe HTML
//
// Each step attaches a common.WireRecipe (curl + Go) for the wire
// reproduction.
func runDemo() {
	serverURL := common.MCPServerURL()

	demo := demokit.New("integration — get-time + ResourceLink download path").
		Dir("integration").
		Description("Walks the integration-server round trip end-to-end: one App tool (get-time, same shape as basic-vanillajs), one plain resource (sample-report.txt — a downloadable text resource demoing the host's ResourceLink + ui/download-file pathway), and the iframe HTML. The fixture mirrors upstream's integration-server: server-side time + downloadable resource, with three iframe-driven Playwright tests upstream ships (Send Message / Send Log / Open Link) all riding the App SDK bridge. Run `make serve` in another terminal first.").
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
		"Any MCP host can connect to the running server. The walkthrough below acts as a scripted host that issues the protocol calls directly through `*mcpkit/client.Client` — no LLM, no browser. The same calls drive the iframe when you run `make demo-app EXAMPLE=integration` in basic-host (see the [centralized guide](../README.md#other-ways-to-test-a-fixture)).",
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
    core.ClientInfo{Name: "integration-host", Version: "1.0"},
)
if err := c.Connect(); err != nil {
    log.Fatalf("connect: %v", err)
}`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		c = client.NewClient(serverURL+"/mcp",
			core.ClientInfo{Name: "integration-host", Version: "1.0"},
		)
		if err := c.Connect(); err != nil {
			fmt.Printf("    ERROR: %v\n    Start the server with: make serve\n", err)
			return nil
		}
		fmt.Printf("    connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)
		return nil
	})

	step2 := demo.Step("tools/list + resources/list — surfaces side-by-side").
		Arrow("Host", "Server", "tools/list + resources/list").
		DashedArrow("Server", "Host", "tools[get-time], resources[ui://get-time/mcp-app.html, resource:///sample-report.txt]").
		Note("Two probes side-by-side. `tools/list` returns just `get-time` (App tool, `_meta.ui.resourceUri` = `ui://get-time/mcp-app.html`). `resources/list` returns BOTH the App's iframe HTML AND a separate `resource:///sample-report.txt` — a plain downloadable text resource. basic-host's `ui/download-file` path resolves the latter via this same `resources/read` call when the iframe asks to download a file.")
	common.WireRecipe(step2,
		`curl -s -X POST `+serverURL+`/mcp -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' \
  | jq '.result.tools[] | {name, _meta}'

curl -s -X POST `+serverURL+`/mcp -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":3,"method":"resources/list","params":{}}' \
  | jq '.result.resources[] | {uri, name, mimeType}'`,
		`tools, _ := c.Call("tools/list", map[string]any{})
resources, _ := c.Call("resources/list", map[string]any{})
fmt.Printf("tools: %s\n", string(tools.Raw))
fmt.Printf("resources: %s\n", string(resources.Raw))`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		toolsRes, err := c.Call("tools/list", map[string]any{})
		if err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		var tl struct {
			Tools []struct {
				Name string         `json:"name"`
				Meta map[string]any `json:"_meta,omitempty"`
			} `json:"tools"`
		}
		json.Unmarshal(toolsRes.Raw, &tl)
		for _, t := range tl.Tools {
			fmt.Printf("    tool: %s  _meta=%v\n", t.Name, t.Meta)
		}
		resRes, err := c.Call("resources/list", map[string]any{})
		if err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		var rl struct {
			Resources []struct {
				URI      string `json:"uri"`
				Name     string `json:"name"`
				MimeType string `json:"mimeType"`
			} `json:"resources"`
		}
		json.Unmarshal(resRes.Raw, &rl)
		for _, r := range rl.Resources {
			fmt.Printf("    resource: %s  (%s, %s)\n", r.URI, r.Name, r.MimeType)
		}
		return nil
	})

	step3 := demo.Step("tools/call get-time — RFC 3339 nano timestamp").
		Arrow("Host", "Server", "tools/call get-time {}").
		DashedArrow("Server", "Host", "structuredContent = { time: \"2026-...\" }").
		Note("Same shape as basic-vanillajs's get-time: the typed handler returns `getTimeOutput{Time: time.Now().UTC().Format(time.RFC3339Nano)}`. The iframe consumes the timestamp; the integration tests upstream ships (Send Message / Send Log / Open Link) all sit on top of this same shape — they exercise the App SDK bridge rather than introduce new server endpoints.")
	common.WireRecipe(step3,
		`curl -s -X POST `+serverURL+`/mcp -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"get-time","arguments":{}}}' \
  | jq '.result.structuredContent'`,
		`tr, _ := c.ToolCallFull("get-time", map[string]any{})
pretty, _ := json.MarshalIndent(tr.StructuredContent, "", "  ")
fmt.Println(string(pretty))`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		tr, err := c.ToolCallFull("get-time", map[string]any{})
		if err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		pretty, _ := json.MarshalIndent(tr.StructuredContent, "    ", "  ")
		fmt.Printf("    structuredContent: %s\n", string(pretty))
		return nil
	})

	step4 := demo.Step("resources/read on resource:///sample-report.txt — ResourceLink content").
		Arrow("Host", "Server", "resources/read { uri: resource:///sample-report.txt }").
		DashedArrow("Server", "Host", "Contents[0] = { text/plain, fresh 'Generated:' line }").
		Note("This is the downloadable text resource — the same call basic-host's `ui/download-file` path issues when the iframe asks to download a file. Each read returns a fresh `Generated: <RFC 3339 nano>` line so a round-trip can be verified. Server-side it's a plain `srv.RegisterResource` with a small text-formatting handler; from the iframe's perspective it's a ResourceLink the bridge resolves opaquely.")
	common.WireRecipe(step4,
		`curl -s -X POST `+serverURL+`/mcp -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":5,"method":"resources/read","params":{"uri":"resource:///sample-report.txt"}}' \
  | jq -r '.result.contents[0].text'`,
		`text, _ := c.ReadResource("resource:///sample-report.txt")
fmt.Println(text)`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		text, err := c.ReadResource("resource:///sample-report.txt")
		if err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		fmt.Printf("    %s\n", text)
		return nil
	})

	step5 := demo.Step("resources/read on ui://get-time/mcp-app.html — the App iframe").
		Arrow("Host", "Server", "resources/read { uri: ui://get-time/mcp-app.html }").
		DashedArrow("Server", "Host", "Contents[0].Text = the iframe HTML").
		Note("The App iframe itself. Upstream's three Playwright interaction tests (Send Message / Send Log / Open Link) all drive the iframe's bridge JS — the server's role reduces to serving this HTML + the get-time tool + the downloadable resource. No CSP block.")
	common.WireRecipe(step5,
		`curl -s -X POST `+serverURL+`/mcp -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":6,"method":"resources/read","params":{"uri":"ui://get-time/mcp-app.html"}}' \
  | jq -r '.result.contents[0].text' | head -c 200`,
		`text, _ := c.ReadResource("ui://get-time/mcp-app.html")
fmt.Printf("%d bytes; first 200:\n%.200s\n", len(text), text)`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		text, err := c.ReadResource("ui://get-time/mcp-app.html")
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
		"integration's iframe sits at the **rich** end of the App-ness spectrum — it exercises the full broad-surface bridge for upstream's three interaction-test cases. The bridge calls its App SDK makes:",
		"",
		"- `app.ontoolresult` for `get-time` — surfaces the timestamp.",
		"- `app.sendMessage` / `app.sendLog` — fire-and-forget bridge calls upstream's tests gate on. mcpkit's role here is bridge-relay only; the messages don't reach the server.",
		"- `app.requestDisplayMode` — opens / closes fullscreen.",
		"- `ui/download-file` — resolves `resource:///sample-report.txt` through `resources/read` (step 4) and hands the bytes to the host's download UI.",
		"",
		"Most of the App-ness here is bridge-resident, not server-resident. This fixture's job is to make sure the SDK plumbing flows end-to-end.",
	)

	demo.Section("Where to look in the code",
		"- `main.go` — fixture is ~130 lines. Two registrations: `ui.RegisterTypedAppTool` for `get-time` (paired with the App iframe), and a plain `srv.RegisterResource` for the `resource:///sample-report.txt` downloadable text — note the resource is intentionally NOT paired with the iframe (it's served alongside, not via the same `_meta.ui.resourceUri` mechanism).",
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
