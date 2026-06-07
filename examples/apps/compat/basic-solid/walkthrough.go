package main

import (
	"encoding/json"
	"fmt"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
)

// runDemo is the basic-solid walkthrough. Acts as a scripted MCP host
// against a server started by `make serve` in another terminal.
//
// Walks four steps:
//
//   1. Connect to the fixture (POST /mcp initialize → session)
//   2. tools/list — verify get-time is advertised + the _meta.ui carries
//      the resourceUri the iframe loads
//   3. tools/call get-time — capture structuredContent and confirm the
//      ISO 8601 timestamp shape
//   4. resources/read on the resourceUri — verify the iframe HTML is
//      served (sanity-check the App→server round trip without basic-host)
//
// Each step attaches two unboxed Verbatim blocks via common.WireRecipe:
// a curl form (copy-pastable into a terminal) and a Go form (the
// equivalent *client.Client call). Both render outside the TUI bordered
// box for mouse-select friendliness.
func runDemo() {
	serverURL := common.MCPServerURL()

	demo := demokit.New("basic-solid — same App, Solid iframe").
		Dir("basic-solid").
		Description("Walks the get-time round trip end-to-end as a scripted MCP client: initialize, tools/list, tools/call, and resources/read on the App's iframe HTML. The fixture mirrors upstream's basic-server-solid example byte-for-byte at the protocol surface, so the trace here is also a faithful upstream-parity check. Run `make serve` in another terminal first.").
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
		"Any MCP host can connect to the running server (Claude Desktop, VS Code, MCPJam, basic-host). The walkthrough below acts as a scripted host that issues the protocol calls directly through `*mcpkit/client.Client` — no LLM, no browser. The same calls drive the iframe when you run `make demo-app EXAMPLE=basic-solid` in basic-host (see the [centralized guide](../README.md#other-ways-to-test-a-fixture)).",
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
    core.ClientInfo{Name: "basic-solid-host", Version: "1.0"},
)
if err := c.Connect(); err != nil {
    log.Fatalf("connect: %v", err)
}
// Connect() handles initialize + notifications/initialized + session header.
fmt.Printf("connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		c = client.NewClient(serverURL+"/mcp",
			core.ClientInfo{Name: "basic-solid-host", Version: "1.0"},
		)
		if err := c.Connect(); err != nil {
			fmt.Printf("    ERROR: %v\n    Start the server with: make serve\n", err)
			return nil
		}
		fmt.Printf("    connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)
		return nil
	})

	step2 := demo.Step("tools/list — verify get-time + its UI resource").
		Arrow("Host", "Server", "tools/list").
		DashedArrow("Server", "Host", "tools[] including get-time").
		Note("The interesting bit isn't the tool list itself — it's `_meta.ui.resourceUri` on get-time. That URI (`ui://get-time/mcp-app.html`) tells the host this tool has an iframe to render. Both the nested form (`_meta.ui.resourceUri`) and the flat form (`_meta.ui/resourceUri`) are emitted for backward compatibility with hosts that read either.")
	common.WireRecipe(step2,
		`curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' \
  | jq '.result.tools[] | {name, _meta}'`,
		`res, _ := c.Call("tools/list", map[string]any{})
// tools/list returns .tools[]; each tool has name + _meta + ...
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

	step3 := demo.Step("tools/call get-time — capture the ISO 8601 timestamp").
		Arrow("Host", "Server", "tools/call get-time {}").
		DashedArrow("Server", "Host", "ToolResult.structuredContent = { time: ... }").
		Note("The handler returns a typed `getTimeOutput{Time: time.Now().UTC().Format(time.RFC3339Nano)}`. The framework marshals it into `structuredContent` automatically. This is the exact payload the iframe receives via the bridge — the only difference between this scripted call and the basic-host flow is that the iframe inlines the timestamp into its DOM.")
	common.WireRecipe(step3,
		`curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get-time","arguments":{}}}' \
  | jq '.result.structuredContent'`,
		`tr, _ := c.ToolCallFull("get-time", map[string]any{})
// tr.StructuredContent is map[string]any; the typed handler emitted
// { "time": "<RFC 3339 nano>" }.
pretty, _ := json.MarshalIndent(tr.StructuredContent, "", "  ")
fmt.Println(string(pretty))`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		tr, err := c.ToolCallFull("get-time", map[string]any{})
		if err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		pretty, _ := json.MarshalIndent(tr.StructuredContent, "      ", "  ")
		fmt.Printf("    structuredContent: %s\n", string(pretty))
		return nil
	})

	step4 := demo.Step("resources/read on ui://get-time/mcp-app.html").
		Arrow("Host", "Server", "resources/read { uri: ui://get-time/mcp-app.html }").
		DashedArrow("Server", "Host", "Contents[0].Text = the iframe HTML").
		Note("Confirms the App's iframe HTML is actually served on the MCP wire. This is what basic-host fetches under the hood once it sees `_meta.ui.resourceUri`. Reading it via the client is a cheap upstream-parity check — the bytes should be identical to upstream's `dist/mcp-app.html` build.")
	common.WireRecipe(step4,
		`curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":4,"method":"resources/read","params":{"uri":"ui://get-time/mcp-app.html"}}' \
  | jq -r '.result.contents[0].text' | head -c 200`,
		`text, _ := c.ReadResource("ui://get-time/mcp-app.html")
fmt.Printf("first 200 bytes: %.200s\n", text)`,
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
		fmt.Printf("    %d bytes total; first 200:\n      %s\n", len(text), preview)
		return nil
	})

	demo.Section("Where to look in the code",
		"- `main.go` — fixture is ~60 lines: one typed tool, one paired UI resource, CORS for browser hosts.",
		"- `walkthrough.go` — this file. Each step's curl + Go recipe is the canonical wire reproduction.",
		"- `../README.md` — narrative + screenshots + the centralized [Other ways to test a fixture](../README.md#other-ways-to-test-a-fixture) guide.",
	)

	_ = step1
	_ = step2
	_ = step3
	_ = step4

	common.SetupRenderer(demo)
	demo.Execute()
}
