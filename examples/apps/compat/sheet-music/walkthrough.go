package main

import (
	"encoding/json"
	"fmt"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
)

// runDemo is the sheet-music walkthrough. Acts as a scripted MCP host
// against a server started by `make serve` in another terminal.
//
// Walks four steps, each one anchored on a fixture-specific protocol
// surface (not generic protocol mechanics — that's basic-vanillajs's
// job):
//
//  1. Connect to the fixture (POST /mcp initialize → session).
//  2. tools/list — focus on `inputSchema.properties.abcNotation.default`.
//     The multi-line, multi-comma ABC notation default landing intact is
//     the whole InputSchemaPatch payoff and the reason this fixture
//     exists. invopop's struct-tag parser would have truncated the
//     default at the first comma; `s.Prop("abcNotation").Default(...)`
//     bypasses that.
//  3. tools/call play-sheet-music with the default ABC — confirm the
//     round trip works and the handler returns the small "Input parsed
//     successfully" text envelope. The interesting wire-level fact is
//     that the input crossed at 184 chars verbatim.
//  4. resources/read on the iframe URI — focus on `_meta.ui.csp.connectDomains`.
//     The walkthrough does NOT dump the HTML body; the pedagogically
//     interesting bit is the CSP allowlist that unblocks abcjs's
//     soundfont fetch. Same family of bug as the transcript fixture's
//     missing `microphone` permission — silent failure if you only
//     trust the visual render.
//
// Each step attaches two unboxed Verbatim blocks via common.WireRecipe:
// a curl form (copy-pastable into a terminal) and a Go form (the
// equivalent *client.Client call).
func runDemo() {
	serverURL := common.MCPServerURL()

	demo := demokit.New("sheet-music — multi-line default + CSP-allowlisted iframe").
		Dir("sheet-music").
		Description("Walks the play-sheet-music round trip end-to-end as a scripted MCP client. The two distinctive things about this fixture are visible on the wire: the InputSchemaPatch default (a multi-line comma-laden ABC notation string that struct-tag reflection would have truncated) and the resource's _meta.ui.csp.connectDomains (the soundfont CDN allowlist that lets abcjs play audio). Run `make serve` in another terminal first.").
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
		"Any MCP host can connect to the running server (Claude Desktop, VS Code, MCPJam, basic-host). The walkthrough below acts as a scripted host that issues the protocol calls directly through `*mcpkit/client.Client` — no LLM, no browser. The same calls drive the iframe when you run `make demo-app EXAMPLE=sheet-music` in basic-host (see the [centralized guide](../README.md#other-ways-to-test-a-fixture)).",
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
    core.ClientInfo{Name: "sheet-music-host", Version: "1.0"},
)
if err := c.Connect(); err != nil {
    log.Fatalf("connect: %v", err)
}
// Connect() handles initialize + notifications/initialized + session header.
fmt.Printf("connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		c = client.NewClient(serverURL+"/mcp",
			core.ClientInfo{Name: "sheet-music-host", Version: "1.0"},
		)
		if err := c.Connect(); err != nil {
			fmt.Printf("    ERROR: %v\n    Start the server with: make serve\n", err)
			return nil
		}
		fmt.Printf("    connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)
		return nil
	})

	step2 := demo.Step("tools/list — verify the multi-line default landed intact").
		Arrow("Host", "Server", "tools/list").
		DashedArrow("Server", "Host", "tools[] including play-sheet-music").
		Note("The pedagogically interesting bit isn't that play-sheet-music exists — it's `inputSchema.properties.abcNotation.default`. The fixture uses `InputSchemaPatch` (`s.Prop(\"abcNotation\").Default(defaultABCNotation)`) to land the 11-line, comma-laden ABC notation default verbatim. invopop's struct-tag parser would have truncated at the first comma. The walkthrough prints just that default field below — confirm every line is present.")
	common.WireRecipe(step2,
		`curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' \
  | jq -r '.result.tools[] | select(.name=="play-sheet-music") | .inputSchema.properties.abcNotation.default'`,
		`res, _ := c.Call("tools/list", map[string]any{})
var out struct {
    Tools []struct {
        Name        string         `+"`json:\"name\"`"+`
        InputSchema map[string]any `+"`json:\"inputSchema\"`"+`
    } `+"`json:\"tools\"`"+`
}
json.Unmarshal(res.Raw, &out)
for _, t := range out.Tools {
    if t.Name == "play-sheet-music" {
        props := t.InputSchema["properties"].(map[string]any)
        abc := props["abcNotation"].(map[string]any)
        fmt.Printf("default:\n%s\n", abc["default"])
    }
}`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		res, err := c.Call("tools/list", map[string]any{})
		if err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		var out struct {
			Tools []struct {
				Name        string         `json:"name"`
				InputSchema map[string]any `json:"inputSchema"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(res.Raw, &out); err != nil {
			fmt.Printf("    ERROR decoding tools/list: %v\n", err)
			return nil
		}
		for _, t := range out.Tools {
			if t.Name != "play-sheet-music" {
				continue
			}
			props, _ := t.InputSchema["properties"].(map[string]any)
			abc, _ := props["abcNotation"].(map[string]any)
			if def, ok := abc["default"].(string); ok {
				fmt.Printf("    inputSchema.properties.abcNotation.default:\n")
				for _, line := range common.SplitLines(def) {
					fmt.Printf("      %s\n", line)
				}
			}
		}
		return nil
	})

	step3 := demo.Step("tools/call play-sheet-music — round-trip the ABC notation").
		Arrow("Host", "Server", "tools/call play-sheet-music { abcNotation: <184 chars> }").
		DashedArrow("Server", "Host", "ToolResult.content[0].text = \"Input parsed successfully.\"").
		Note("The server-side handler is intentionally minimal — it returns a small text envelope confirming the parse. The wire-level lesson is that the full 184-char ABC notation crosses verbatim and is unmarshaled into the typed `playSheetMusicInput.ABCNotation` field. The iframe does the actual rendering + audio playback work; see step 4 for what makes that work.")
	common.WireRecipe(step3,
		`curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"play-sheet-music","arguments":{"abcNotation":"X:1\nT:Twinkle, Twinkle Little Star\nM:4/4\nL:1/4\nK:C\nC C G G | A A G2 | F F E E | D D C2 |\nG G F F | E E D2 | G G F F | E E D2 |\nC C G G | A A G2 | F F E E | D D C2 |"}}}' \
  | jq -r '.result.content[0].text'`,
		`tr, _ := c.ToolCallFull("play-sheet-music", map[string]any{
    "abcNotation": defaultABCNotation, // the const from main.go
})
// tr.Content[0].Text is the small text envelope.
fmt.Printf("server said: %s\n", tr.Content[0].Text)`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		tr, err := c.ToolCallFull("play-sheet-music", map[string]any{
			"abcNotation": defaultABCNotation,
		})
		if err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		if len(tr.Content) > 0 {
			fmt.Printf("    server said: %s\n", tr.Content[0].Text)
		}
		fmt.Printf("    (sent %d-char ABC notation; round-tripped through the typed handler)\n", len(defaultABCNotation))
		return nil
	})

	step4 := demo.Step("resources/read — the CSP allowlist that unblocks audio").
		Arrow("Host", "Server", "resources/read { uri: ui://sheet-music/mcp-app.html }").
		DashedArrow("Server", "Host", "Contents[0]._meta.ui.csp.connectDomains").
		Note("This walkthrough does NOT dump the HTML body — it's a build artifact mirrored verbatim from upstream. The pedagogically interesting bit is `_meta.ui.csp.connectDomains` on the resource read response. abcjs streams soundfonts from `paulrosen.github.io` when the user clicks ▶ Play; without that origin on the iframe's `connect-src` allowlist, basic-host's CSP blocks the fetch and audio silently fails. The fixture declares it on the resource _meta — same shape upstream's sheet-music-server uses, same shape every CSP-aware MCP host can read.")
	common.WireRecipe(step4,
		`curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":4,"method":"resources/read","params":{"uri":"ui://sheet-music/mcp-app.html"}}' \
  | jq '.result.contents[0]._meta'`,
		`var raw struct {
    Contents []struct {
        Meta json.RawMessage `+"`json:\"_meta\"`"+`
    } `+"`json:\"contents\"`"+`
}
res, _ := c.Call("resources/read", map[string]any{
    "uri": "ui://sheet-music/mcp-app.html",
})
json.Unmarshal(res.Raw, &raw)
// The bytes of _meta are the wire-level proof: csp.connectDomains
// lists the soundfont origin.
fmt.Printf("_meta: %s\n", string(raw.Contents[0].Meta))`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		res, err := c.Call("resources/read", map[string]any{
			"uri": "ui://sheet-music/mcp-app.html",
		})
		if err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		var raw struct {
			Contents []struct {
				Meta json.RawMessage `json:"_meta"`
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
		if len(raw.Contents[0].Meta) == 0 {
			fmt.Printf("    _meta is empty — CSP allowlist would NOT be applied; audio playback would silently fail\n")
			return nil
		}
		pretty, _ := json.MarshalIndent(json.RawMessage(raw.Contents[0].Meta), "      ", "  ")
		fmt.Printf("    _meta:\n      %s\n", string(pretty))
		return nil
	})

	demo.Section("What the App iframe does with all this",
		"The host → iframe handoff mechanics (`tools/call` → `resources/read` → sandboxed iframe → `postMessage`) are covered once in [The basic-host bridge dance](../README.md#the-basic-host-bridge-dance). Steps 1-4 above are the floor every MCP Apps interaction starts from.",
		"",
		"sheet-music's iframe sits at the **bare-minimum** end of the App-ness spectrum, but with one extra resource-level declaration that's the whole reason this fixture exists in our parity coverage. The bridge calls its App SDK makes:",
		"",
		"- `app.ontoolresult` — receives the typed text content (`\"Input parsed successfully.\"`) plus the input's ABC notation, hands them to abcjs.",
		"- `app.callServerTool({name: \"play-sheet-music\", arguments: {abcNotation}})` — when the iframe wants to re-validate notation (e.g., the user edited a piece in a future fixture). Today the demo passes the default ABC and renders.",
		"",
		"What makes this fixture different from `basic-vanillajs` isn't the bridge calls — it's the **resource `_meta`**: `_meta.ui.csp.connectDomains: [\"https://paulrosen.github.io\"]` on step 4's response is what unblocks abcjs streaming soundfont samples from the CDN. Without that line in the fixture's ResourceHandler, the iframe renders sheet music silently but the play button does nothing — see [the CSP connect-src contract](../sheet-music/README.md#the-csp-connect-src-contract) section of the README.",
	)

	demo.Section("Where to look in the code",
		"- `main.go` — the InputSchemaPatch landing the default + the ResourceHandler setting `_meta.ui.csp.connectDomains`. Both are visible in single contiguous edits.",
		"- `walkthrough.go` — this file. Each step's curl + Go recipe is the canonical wire reproduction.",
		"- `../README.md` — narrative + the [CSP connect-src contract](../sheet-music/README.md#the-csp-connect-src-contract) section explains why the resource `_meta` matters.",
	)

	_ = step1
	_ = step2
	_ = step3
	_ = step4

	common.SetupRenderer(demo)
	demo.Execute()
}
