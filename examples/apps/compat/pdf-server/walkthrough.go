package main

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
)

// runDemo is the pdf-server walkthrough. Acts as a scripted MCP host
// against a server started by `just serve` in another terminal.
//
// Walks five steps:
//
//  1. Connect to the fixture (initialize → session)
//  2. tools/list — verify the 9-tool surface (list_pdfs, read_pdf_bytes,
//     display_pdf, interact, poll_pdf_commands, submit_page_data,
//     submit_save_data, submit_viewer_state, save_pdf). Inspect the
//     visibility / resourceUri shape on each.
//  3. tools/call list_pdfs {} — base inventory the model uses to pick
//     which PDF to open.
//  4. tools/call display_pdf {url: arxiv default} — mints a viewUUID,
//     surfaces interactEnabled + writable in _meta. The iframe consumes
//     the URI from _meta.ui.resourceUri and starts long-polling.
//  5. resources/read on the iframe — sanity-check.
//
// Deliberately NOT exercised in the scripted walkthrough: the
// `interact` → `poll_pdf_commands` → `submit_*` rendezvous. Those
// require a real iframe responding to the bridge; a scripted client
// would block on the long-poll waiter. The narrative section below
// spells out what would happen in basic-host.
//
// Each step attaches a common.WireRecipe (curl + Go) for the wire
// reproduction.
func runDemo() {
	serverURL := common.MCPServerURL()

	demo := demokit.New("pdf-server — 9-tool surface, command queue, long-poll").
		Dir("pdf-server").
		Description("Walks the most complex fixture in the compat suite: 9 tools wired through a per-viewUUID command queue, long-poll endpoint, and viewer rendezvous. The scripted walkthrough exercises the non-blocking paths (list_pdfs, display_pdf, read_pdf_bytes); the narrative section explains the interact / poll_pdf_commands / submit_* rendezvous that drives the iframe in basic-host. Run `just serve` in another terminal first.").
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
		"Any MCP host can connect to the running server. The walkthrough below acts as a scripted host that issues the protocol calls directly through `*mcpkit/client.Client` — no LLM, no browser, no PDF viewer. The same calls drive the iframe when you run `just demo-app EXAMPLE=pdf-server` in basic-host (see the [centralized guide](../README.md#other-ways-to-test-a-fixture)).",
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
    core.ClientInfo{Name: "pdf-server-host", Version: "1.0"},
)
if err := c.Connect(); err != nil {
    log.Fatalf("connect: %v", err)
}`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		c = client.NewClient(serverURL+"/mcp",
			core.ClientInfo{Name: "pdf-server-host", Version: "1.0"},
		)
		if err := c.Connect(); err != nil {
			fmt.Printf("    ERROR: %v\n    Start the server with: just serve\n", err)
			return nil
		}
		fmt.Printf("    connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)
		return nil
	})

	step2 := demo.Step("tools/list — 9-tool surface").
		Arrow("Host", "Server", "tools/list").
		DashedArrow("Server", "Host", "tools[9]").
		Note("Two roles on this surface. The 4 model-visible tools (`list_pdfs`, `display_pdf`, `save_pdf`, plus `interact` once a PDF is displayed) are how the model drives the viewer. The 5 App-only tools (`read_pdf_bytes`, `poll_pdf_commands`, `submit_page_data`, `submit_save_data`, `submit_viewer_state`) carry `_meta.ui.visibility=[\"app\"]` — they're the iframe's side of the rendezvous. `display_pdf` carries `_meta.ui.resourceUri` (`ui://pdf-viewer/mcp-app.html`) — its result spawns the iframe.")
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
sort.Slice(out.Tools, func(i, j int) bool { return out.Tools[i].Name < out.Tools[j].Name })
for _, t := range out.Tools {
    fmt.Printf("  %s\n", t.Name)
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
		sort.Slice(out.Tools, func(i, j int) bool { return out.Tools[i].Name < out.Tools[j].Name })
		fmt.Printf("    %d tools registered:\n", len(out.Tools))
		for _, t := range out.Tools {
			role := "model+app"
			ui, _ := t.Meta["ui"].(map[string]any)
			if vis, ok := ui["visibility"].([]any); ok {
				for _, v := range vis {
					if v == "app" {
						role = "app-only"
					}
				}
			}
			hasURI := ""
			if _, ok := ui["resourceUri"]; ok {
				hasURI = "  (carries resourceUri)"
			}
			fmt.Printf("      %-22s  %s%s\n", t.Name, role, hasURI)
		}
		return nil
	})

	step3 := demo.Step("tools/call list_pdfs {} — PDF inventory").
		Arrow("Host", "Server", "tools/call list_pdfs {}").
		DashedArrow("Server", "Host", "structuredContent = { localFiles: [], allowedDirectories: [] }").
		Note("Discovery surface — what PDFs can `display_pdf` open. The mcpkit fixture ships an empty allowlist by default (no `--allow-file` / `--allow-dir` CLI flags); upstream's same defaults. The text content (`\"Any remote PDF accessible via HTTPS can also be loaded dynamically\"`) tells the model it can pass arbitrary HTTPS URLs without an allowlist entry.")
	common.WireRecipe(step3,
		`curl -s -X POST `+serverURL+`/mcp -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_pdfs","arguments":{}}}' \
  | jq '.result | {text: .content[0].text, structuredContent}'`,
		`tr, _ := c.ToolCallFull("list_pdfs", map[string]any{})
fmt.Printf("text: %q\nstructured: %v\n", tr.Content[0].Text, tr.StructuredContent)`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		tr, err := c.ToolCallFull("list_pdfs", map[string]any{})
		if err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		if len(tr.Content) > 0 {
			fmt.Printf("    text: %q\n", tr.Content[0].Text)
		}
		pretty, _ := json.MarshalIndent(tr.StructuredContent, "    ", "  ")
		fmt.Printf("    structuredContent: %s\n", string(pretty))
		return nil
	})

	step4 := demo.Step("tools/call display_pdf {url: \"arxiv 1706.03762\"}").
		Arrow("Host", "Server", "tools/call display_pdf {url: \"https://arxiv.org/pdf/1706.03762\"}").
		DashedArrow("Server", "Host", "structuredContent = { viewUUID, url, initialPage, totalBytes } + _meta.ui {resourceUri, viewUUID, interactEnabled, writable}").
		Note("Mints a fresh viewUUID and registers a per-UUID command queue + waiter. The result carries `_meta.ui.resourceUri` (the iframe HTML) AND extra `_meta.ui.viewUUID` / `_meta.ui.interactEnabled` / `_meta.ui.writable` — strings the upstream-bound Playwright tests substring-check for. basic-host fetches the resourceUri, opens the iframe, then the iframe long-polls `poll_pdf_commands` to receive interact commands keyed on this viewUUID.")
	common.WireRecipe(step4,
		`curl -s -X POST `+serverURL+`/mcp -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"display_pdf","arguments":{"url":"https://arxiv.org/pdf/1706.03762"}}}' \
  | jq '.result | {structuredContent, meta: ._meta}'`,
		`tr, _ := c.ToolCallFull("display_pdf", map[string]any{
    "url": "https://arxiv.org/pdf/1706.03762",
})
pretty, _ := json.MarshalIndent(tr.StructuredContent, "", "  ")
fmt.Println(string(pretty))
fmt.Printf("_meta: %v\n", tr.Meta)`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		tr, err := c.ToolCallFull("display_pdf", map[string]any{
			"url": "https://arxiv.org/pdf/1706.03762",
		})
		if err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		pretty, _ := json.MarshalIndent(tr.StructuredContent, "    ", "  ")
		fmt.Printf("    structuredContent: %s\n", string(pretty))
		if tr.Meta != nil {
			m, _ := json.MarshalIndent(tr.Meta, "    ", "  ")
			fmt.Printf("    _meta: %s\n", string(m))
		}
		return nil
	})

	step5 := demo.Step("resources/read on ui://pdf-viewer/mcp-app.html").
		Arrow("Host", "Server", "resources/read { uri: ui://pdf-viewer/mcp-app.html }").
		DashedArrow("Server", "Host", "Contents[0].Text = the iframe HTML").
		Note("Sanity-check the App's iframe is served on the wire. No CSP — the iframe pulls PDF bytes through `read_pdf_bytes` (server-side proxy) rather than fetching them directly from arxiv. The proxy approach lets the fixture support local files / range requests / size caps without exposing the iframe's CSP to every PDF host.")
	common.WireRecipe(step5,
		`curl -s -X POST `+serverURL+`/mcp -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":5,"method":"resources/read","params":{"uri":"ui://pdf-viewer/mcp-app.html"}}' \
  | jq -r '.result.contents[0].text' | head -c 200`,
		`text, _ := c.ReadResource("ui://pdf-viewer/mcp-app.html")
fmt.Printf("%d bytes; first 200:\n%.200s\n", len(text), text)`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		text, err := c.ReadResource("ui://pdf-viewer/mcp-app.html")
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

	demo.Section("What's NOT in this walkthrough — the interact rendezvous",
		"The 5 App-only tools (`read_pdf_bytes`, `poll_pdf_commands`, `submit_page_data`, `submit_save_data`, `submit_viewer_state`) plus `interact` drive a per-viewUUID command queue with long-poll + rendezvous semantics. A scripted client like this walkthrough can't run them without an iframe on the other side — the long-poll waits would block indefinitely (or time out). What happens in basic-host:",
		"",
		"1. **Model calls `interact {viewUUID, action: \"navigate\", page: 5}`.** Server enqueues a `navigate` command on `viewUUID`'s queue and creates a Waiter for the response — the tool call blocks server-side.",
		"2. **Iframe's `poll_pdf_commands {viewUUID}` long-poll unblocks** with the new command, applies it (jumps to page 5), and computes the page's text content.",
		"3. **Iframe calls `submit_page_data {viewUUID, page: 5, text: ...}`** — that submission resolves the Waiter from step 1, and the model's `interact` call finally returns with the page text in its content.",
		"",
		"Same shape for `interact {action: \"add_annotations\", ...}` (rendezvous via `submit_save_data`), `interact {action: \"get_viewer_state\"}` (rendezvous via `submit_viewer_state`), and `read_pdf_bytes` (proxy-fetch with HTTP Range requests, max 512KB per chunk, base64-encoded bytes in the result). All implementations live in `queue.go` (per-UUID `hub` + waiter map) and `tools.go` (dispatch + content unwrap).",
		"",
		"Run the upstream Playwright suite end-to-end via `just test-apps-playwright EXAMPLE=pdf-server` to see all the variations exercised against a real iframe.",
	)

	demo.Section("What the App iframe does with all this",
		"The host → iframe handoff mechanics (`tools/call` → `resources/read` → sandboxed iframe → `postMessage`) are covered once in [The basic-host bridge dance](../README.md#the-basic-host-bridge-dance). Steps 1-5 above are the floor every MCP Apps interaction starts from.",
		"",
		"pdf-server's iframe sits at the **endgame** end of the App-ness spectrum — the deepest bridge surface of any fixture in the compat suite. The bridge calls its App SDK makes:",
		"",
		"- `app.ontoolresult` for `display_pdf` — reads viewUUID from `_meta.ui`, starts the PDF.js viewer.",
		"- `app.callServerTool({name: \"read_pdf_bytes\", arguments: {url, offset, byteCount}})` — chunks the PDF in via HTTP Range, max 512KB per call.",
		"- `app.callServerTool({name: \"poll_pdf_commands\", arguments: {viewUUID}})` — long-poll loop. Receives commands enqueued by `interact` calls; applies them via PDF.js APIs; submits responses via the matching `submit_*` tool.",
		"- `app.callServerTool({name: \"submit_page_data\" / \"submit_save_data\" / \"submit_viewer_state\"})` — closes each rendezvous so the model's `interact` call unblocks with results.",
		"- `app.getHostContext()` + `app.onhostcontextchanged` — applies host theme + safeAreaInsets to the viewer chrome.",
		"",
		"NO `app.registerTool` — the App doesn't expose app-side tools back to the host. The iframe is a stateful PDF.js runtime that the App SDK wraps through the rendezvous machinery.",
	)

	demo.Section("Where to look in the code",
		"- `main.go` (~100 lines) — top-level wiring. Hand-off to `registerAllTools` in `tools.go` and the `hub` in `queue.go`.",
		"- `tools.go` (~890 lines) — all 9 tool registrations + interact action dispatcher. Worth reading top-to-bottom to see how the `interact` action enum maps onto the queue commands.",
		"- `queue.go` — per-viewUUID command queue + long-poll waiter + the submit_* rendezvous. The shape that makes the model's `interact` block translate into the iframe's `poll_pdf_commands` unblock translate into the iframe's `submit_*` translate into the model's `interact` return.",
		"- `bytes.go` — HTTP range proxy for `read_pdf_bytes` (allows local file paths via `--allow-file` / `--allow-dir` flags; remote HTTPS by default).",
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
