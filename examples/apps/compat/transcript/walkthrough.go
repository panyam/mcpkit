package main

import (
	"encoding/json"
	"fmt"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
)

// runDemo is the transcript walkthrough. Acts as a scripted MCP host
// against a server started by `make serve` in another terminal.
//
// Walks four steps, each one anchored on a fixture-specific protocol
// surface (not generic protocol mechanics — that's basic-vanillajs's
// job):
//
//  1. Connect to the fixture (POST /mcp initialize → session).
//  2. tools/list — verify transcribe is advertised.
//  3. tools/call transcribe — capture the small `{status: "ready", ...}`
//     text envelope the Go handler returns synchronously. The actual
//     transcription work happens in the iframe via the Web Speech API;
//     the wire call is just the open-the-UI signal.
//  4. resources/read on the iframe URI — focus on `_meta.ui.permissions`.
//     The walkthrough does NOT dump the HTML body; the pedagogically
//     interesting bit is the Permission-Policy declaration that lets the
//     iframe ask for microphone access. Without that _meta block, basic-host
//     renders the iframe with no policy grant and `recognition.start()`
//     silently fails (no mic prompt, no transcription). Same family of
//     bug as the sheet-music fixture's missing CSP allowlist.
//
// Each step attaches two unboxed Verbatim blocks via common.WireRecipe:
// a curl form (copy-pastable into a terminal) and a Go form (the
// equivalent *client.Client call).
func runDemo() {
	serverURL := common.MCPServerURL()

	demo := demokit.New("transcript — Web Speech App with Permission-Policy _meta").
		Dir("transcript").
		Description("Walks the transcribe round trip end-to-end as a scripted MCP client. The distinctive thing about this fixture is visible on the wire: the resource's _meta.ui.permissions block (microphone + clipboardWrite). Without that declaration, basic-host loads the iframe with no Permission-Policy grant and the browser blocks recognition.start() silently. Run `make serve` in another terminal first.").
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
		"Any MCP host can connect to the running server (Claude Desktop, VS Code, MCPJam, basic-host). The walkthrough below acts as a scripted host that issues the protocol calls directly through `*mcpkit/client.Client` — no LLM, no browser. The same calls drive the iframe when you run `make demo-app EXAMPLE=transcript` in basic-host (see the [centralized guide](../README.md#other-ways-to-test-a-fixture)).",
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
    core.ClientInfo{Name: "transcript-host", Version: "1.0"},
)
if err := c.Connect(); err != nil {
    log.Fatalf("connect: %v", err)
}
// Connect() handles initialize + notifications/initialized + session header.
fmt.Printf("connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		c = client.NewClient(serverURL+"/mcp",
			core.ClientInfo{Name: "transcript-host", Version: "1.0"},
		)
		if err := c.Connect(); err != nil {
			fmt.Printf("    ERROR: %v\n    Start the server with: make serve\n", err)
			return nil
		}
		fmt.Printf("    connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)
		return nil
	})

	step2 := demo.Step("tools/list — verify transcribe + its UI resource").
		Arrow("Host", "Server", "tools/list").
		DashedArrow("Server", "Host", "tools[] including transcribe").
		Note("`transcribe` has an empty input schema — the tool is the iframe-open signal, not a parameterized call. `_meta.ui.resourceUri` points at the iframe HTML the host loads.")
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

	step3 := demo.Step("tools/call transcribe — the iframe-open signal").
		Arrow("Host", "Server", "tools/call transcribe {}").
		DashedArrow("Server", "Host", "ToolResult.content[0].text = `{\"status\":\"ready\",...}`").
		Note("The server-side handler is intentionally minimal — `transcribeReady` is a fixed string mirroring upstream's transcript-server. All the real transcription work happens in the iframe via the Web Speech API; this call just signals the host \"open my UI now.\" The iframe-side work is invisible on the wire; what's visible is the small text envelope returned synchronously.")
	common.WireRecipe(step3,
		`curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"transcribe","arguments":{}}}' \
  | jq -r '.result.content[0].text'`,
		`tr, _ := c.ToolCallFull("transcribe", map[string]any{})
// tr.Content[0].Text is `+"`{\"status\":\"ready\",\"message\":\"Transcription UI opened. Speak into your microphone.\"}`"+`
fmt.Printf("server said: %s\n", tr.Content[0].Text)`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		tr, err := c.ToolCallFull("transcribe", map[string]any{})
		if err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		if len(tr.Content) > 0 {
			fmt.Printf("    server said: %s\n", tr.Content[0].Text)
		}
		return nil
	})

	step4 := demo.Step("resources/read — the Permission-Policy block that unblocks the mic").
		Arrow("Host", "Server", "resources/read { uri: ui://transcript/mcp-app.html }").
		DashedArrow("Server", "Host", "Contents[0]._meta.ui.permissions").
		Note("This walkthrough does NOT dump the HTML body — it's a build artifact mirrored verbatim from upstream. The pedagogically interesting bit is `_meta.ui.permissions` on the resource read response. The iframe calls `recognition.start()` (Web Speech API) and the copy-transcript button (Clipboard API); both need Permission-Policy grants on the iframe sandbox. The Go fixture declares them on the resource _meta — same shape upstream's transcript-server uses, same shape every Permission-Policy-aware MCP host can read.")
	common.WireRecipe(step4,
		`curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":4,"method":"resources/read","params":{"uri":"ui://transcript/mcp-app.html"}}' \
  | jq '.result.contents[0]._meta'`,
		`var raw struct {
    Contents []struct {
        Meta json.RawMessage `+"`json:\"_meta\"`"+`
    } `+"`json:\"contents\"`"+`
}
res, _ := c.Call("resources/read", map[string]any{
    "uri": "ui://transcript/mcp-app.html",
})
json.Unmarshal(res.Raw, &raw)
// _meta.ui.permissions is what the host reads to set the iframe's allow= attribute.
fmt.Printf("_meta: %s\n", string(raw.Contents[0].Meta))`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		res, err := c.Call("resources/read", map[string]any{
			"uri": "ui://transcript/mcp-app.html",
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
			fmt.Printf("    _meta is empty — iframe would NOT get Permission-Policy grants; recognition.start() would silently fail\n")
			return nil
		}
		pretty, _ := json.MarshalIndent(json.RawMessage(raw.Contents[0].Meta), "      ", "  ")
		fmt.Printf("    _meta:\n      %s\n", string(pretty))
		return nil
	})

	demo.Section("What the App iframe does with all this",
		"The host → iframe handoff mechanics (`tools/call` → `resources/read` → sandboxed iframe → `postMessage`) are covered once in [The basic-host bridge dance](../README.md#the-basic-host-bridge-dance). Steps 1-4 above are the floor every MCP Apps interaction starts from.",
		"",
		"transcript's iframe sits at the **bare-minimum** end of the App-ness spectrum, but with one extra resource-level declaration that's the whole reason this fixture exists in our parity coverage. The bridge calls its App SDK makes:",
		"",
		"- `app.ontoolresult` — receives the synchronous `{status:\"ready\",...}` envelope and shows the recording controls.",
		"- `app.callServerTool({name: \"transcribe\"})` on button click — the iframe-opens-recording-UI signal.",
		"",
		"What makes this fixture different from `basic-vanillajs` isn't the bridge calls — it's the **resource `_meta`**: `_meta.ui.permissions: { microphone: {}, clipboardWrite: {} }` on step 4's response is what tells basic-host to grant the iframe Web Speech API access and clipboard write. Without that line in the fixture's ResourceHandler, the iframe loads but `recognition.start()` silently fails (no browser mic prompt). See [the iframe permission contract](../transcript/README.md#the-iframe-permission-contract) section of the README.",
	)

	demo.Section("Where to look in the code",
		"- `main.go` — the ResourceHandler setting `_meta.ui.permissions`. The microphone + clipboardWrite fields are visible in a single contiguous edit.",
		"- `walkthrough.go` — this file. Each step's curl + Go recipe is the canonical wire reproduction.",
		"- `../README.md` — narrative + the [iframe permission contract](../transcript/README.md#the-iframe-permission-contract) section explains why the resource `_meta` matters.",
	)

	_ = step1
	_ = step2
	_ = step3
	_ = step4

	common.SetupRenderer(demo)
	demo.Execute()
}
