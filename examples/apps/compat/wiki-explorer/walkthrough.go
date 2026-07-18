package main

import (
	"encoding/json"
	"fmt"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
)

// runDemo is the wiki-explorer walkthrough. Acts as a scripted MCP host
// against a server started by `just serve` in another terminal.
//
// Walks six steps:
//
//  1. Connect to the fixture (initialize → session)
//  2. tools/list — verify get-first-degree-links carries _meta.ui.resourceUri,
//     and inspect the OutputSchemaPatch that lands the nullable `error`
//     anyOf shape (mirrors upstream's z.string().nullable()).
//  3. tools/call get-first-degree-links {} — live Wikipedia round trip
//     against the default URL (Model_Context_Protocol page). Records
//     ~383 outbound links.
//  4. tools/call get-first-degree-links {url: Anthropic} — same call
//     shape against a different article. This is what the iframe
//     issues when expanding a node.
//  5. tools/call get-first-degree-links {url: invalid} — surfaces the
//     non-nil `error` string + empty links array via the never-Go-error
//     contract (matches upstream's catch block).
//  6. resources/read on the iframe resourceUri — sanity-check the HTML
//     is served (no CSP needed — fetcher runs server-side).
//
// Each step attaches a common.WireRecipe (curl + Go) for the wire
// reproduction.
func runDemo() {
	serverURL := common.MCPServerURL()

	demo := demokit.New("wiki-explorer — Wikipedia link graph over MCP Apps").
		Dir("wiki-explorer").
		Description("Walks the get-first-degree-links round trip end-to-end as a scripted MCP client: initialize, tools/list, three tools/call shapes (default URL, different article, invalid URL → string error path), and resources/read. The fixture mirrors upstream's wiki-explorer-server example: server-side Wikipedia HTML fetch + regex link extraction (~380 outbound links for the default MCP page), nullable `error` string in the result, force-directed graph rendered iframe-side. Run `just serve` in another terminal first.").
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
		"Any MCP host can connect to the running server (Claude Desktop, VS Code, MCPJam, basic-host). The walkthrough below acts as a scripted host that issues the protocol calls directly through `*mcpkit/client.Client` — no LLM, no browser. The same calls drive the iframe when you run `just demo-app EXAMPLE=wiki-explorer` in basic-host (see the [centralized guide](../README.md#other-ways-to-test-a-fixture)).",
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
    core.ClientInfo{Name: "wiki-explorer-host", Version: "1.0"},
)
if err := c.Connect(); err != nil {
    log.Fatalf("connect: %v", err)
}`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		c = client.NewClient(serverURL+"/mcp",
			core.ClientInfo{Name: "wiki-explorer-host", Version: "1.0"},
		)
		if err := c.Connect(); err != nil {
			fmt.Printf("    ERROR: %v\n    Start the server with: just serve\n", err)
			return nil
		}
		fmt.Printf("    connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)
		return nil
	})

	step2 := demo.Step("tools/list — verify get-first-degree-links + nullable error shape").
		Arrow("Host", "Server", "tools/list").
		DashedArrow("Server", "Host", "tools[] with the get-first-degree-links output schema").
		Note("Two things worth calling out on the wire. (1) `_meta.ui.resourceUri` (`ui://wiki-explorer/mcp-app.html`) tags the tool as an App — basic-host fetches that URI to fill the iframe. (2) `outputSchema.properties.error` is `{anyOf: [{type:\"string\"}, {type:\"null\"}]}` — mirrors upstream's `z.string().nullable()`. mcpkit lands the nullable shape via `OutputSchemaPatch` + `PropertyBuilder.Replace(anyOf)` since `*string` reflection alone emits `\"type\": \"string\"` only.")
	common.WireRecipe(step2,
		`curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' \
  | jq '.result.tools[] | {name, errorSchema: .outputSchema.properties.error}'`,
		`res, _ := c.Call("tools/list", map[string]any{})
var out struct {
    Tools []struct {
        Name         string         `+"`json:\"name\"`"+`
        OutputSchema map[string]any `+"`json:\"outputSchema\"`"+`
    } `+"`json:\"tools\"`"+`
}
json.Unmarshal(res.Raw, &out)
for _, t := range out.Tools {
    props := t.OutputSchema["properties"].(map[string]any)
    fmt.Printf("  %s\n    error schema: %v\n", t.Name, props["error"])
}`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		res, err := c.Call("tools/list", map[string]any{})
		if err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		var out struct {
			Tools []struct {
				Name         string         `json:"name"`
				OutputSchema map[string]any `json:"outputSchema"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(res.Raw, &out); err != nil {
			fmt.Printf("    ERROR decoding: %v\n", err)
			return nil
		}
		for _, t := range out.Tools {
			fmt.Printf("    %s\n", t.Name)
			if props, ok := t.OutputSchema["properties"].(map[string]any); ok {
				if e, ok := props["error"]; ok {
					pretty, _ := json.MarshalIndent(e, "      ", "  ")
					fmt.Printf("      error schema: %s\n", string(pretty))
				}
			}
		}
		return nil
	})

	step3 := demo.Step("tools/call get-first-degree-links {} — default Wikipedia article").
		Arrow("Host", "Server", "tools/call get-first-degree-links {}").
		DashedArrow("Server", "Host", "structuredContent = { page, links[~380], error: null }").
		Note("Live network round trip — the server fetches `https://en.wikipedia.org/wiki/Model_Context_Protocol`, regex-extracts every `<a href=\"/wiki/...\">` link, filters out Wikipedia: / Help: / File: / etc. namespace pages and self-links, dedupes, and returns one node per outbound article. ~380 links for the default page. Wikipedia returns 403 without a User-Agent header per their policy; the fetcher sets one. The iframe renders these as a force-directed graph; the model can read the same list to plan further exploration.")
	common.WireRecipe(step3,
		`curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get-first-degree-links","arguments":{}}}' \
  | jq '.result.structuredContent | {page, link_count: (.links|length), first_5: [.links[0:5][].title], error}'`,
		`tr, _ := c.ToolCallFull("get-first-degree-links", map[string]any{})
sc := tr.StructuredContent.(map[string]any)
links := sc["links"].([]any)
fmt.Printf("page: %v\n", sc["page"])
fmt.Printf("links: %d\n", len(links))
fmt.Printf("error: %v\n", sc["error"])`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		tr, err := c.ToolCallFull("get-first-degree-links", map[string]any{})
		if err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		sc, _ := tr.StructuredContent.(map[string]any)
		if sc == nil {
			fmt.Printf("    structuredContent missing\n")
			return nil
		}
		page, _ := sc["page"].(map[string]any)
		links, _ := sc["links"].([]any)
		fmt.Printf("    page: %v\n", page)
		fmt.Printf("    links: %d\n", len(links))
		fmt.Printf("    error: %v\n", sc["error"])
		previewN := 5
		if previewN > len(links) {
			previewN = len(links)
		}
		titles := make([]string, 0, previewN)
		for i := 0; i < previewN; i++ {
			if l, ok := links[i].(map[string]any); ok {
				if t, ok := l["title"].(string); ok {
					titles = append(titles, t)
				}
			}
		}
		fmt.Printf("    first %d link titles: %v\n", previewN, titles)
		return nil
	})

	step4 := demo.Step("tools/call get-first-degree-links {url: \"Anthropic\"} — node expansion").
		Arrow("Host", "Server", "tools/call get-first-degree-links {url:\"https://en.wikipedia.org/wiki/Anthropic\"}").
		DashedArrow("Server", "Host", "structuredContent = { page: Anthropic, links[...], error: null }").
		Note("Same call shape, different article. This is what the iframe issues when a user clicks a node in the graph to expand its first-degree neighbours, or types a Wikipedia URL into the App's search box. The bridge call is `app.callServerTool({name: \"get-first-degree-links\", arguments: {url: \"...\"}})`.")
	common.WireRecipe(step4,
		`curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"get-first-degree-links","arguments":{"url":"https://en.wikipedia.org/wiki/Anthropic"}}}' \
  | jq '.result.structuredContent | {page, link_count: (.links|length), error}'`,
		`tr, _ := c.ToolCallFull("get-first-degree-links", map[string]any{
    "url": "https://en.wikipedia.org/wiki/Anthropic",
})
sc := tr.StructuredContent.(map[string]any)
links := sc["links"].([]any)
fmt.Printf("page: %v, links: %d\n", sc["page"], len(links))`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		tr, err := c.ToolCallFull("get-first-degree-links", map[string]any{
			"url": "https://en.wikipedia.org/wiki/Anthropic",
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
		page, _ := sc["page"].(map[string]any)
		links, _ := sc["links"].([]any)
		fmt.Printf("    page: %v\n", page)
		fmt.Printf("    links: %d\n", len(links))
		fmt.Printf("    error: %v\n", sc["error"])
		return nil
	})

	step5 := demo.Step("tools/call get-first-degree-links {url: invalid} — error path").
		Arrow("Host", "Server", "tools/call get-first-degree-links {url:\"https://example.com/foo\"}").
		DashedArrow("Server", "Host", "structuredContent = { page, links: [], error: \"Not a valid Wikipedia URL\" }").
		Note("Invalid URLs (non-Wikipedia hosts, 404s, network errors) land in the nullable `error` string rather than firing an isError tool result. Matches upstream's catch-block contract: the tool always succeeds on the wire; the iframe + model read `.error` to decide what to render. mcpkit's typed handler returns `(out, nil)` even on the failure path; the `Error: &msg` field carries the string.")
	common.WireRecipe(step5,
		`curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"get-first-degree-links","arguments":{"url":"https://example.com/foo"}}}' \
  | jq '.result | {isError, structuredContent: {page, error: .structuredContent.error}}'`,
		`tr, _ := c.ToolCallFull("get-first-degree-links", map[string]any{
    "url": "https://example.com/foo",
})
sc := tr.StructuredContent.(map[string]any)
fmt.Printf("isError=%v (always false), error string=%v\n", tr.IsError, sc["error"])`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		tr, err := c.ToolCallFull("get-first-degree-links", map[string]any{
			"url": "https://example.com/foo",
		})
		if err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		sc, _ := tr.StructuredContent.(map[string]any)
		fmt.Printf("    isError=%v (always false on the wire-error path)\n", tr.IsError)
		if sc != nil {
			fmt.Printf("    page: %v\n", sc["page"])
			fmt.Printf("    error string: %v\n", sc["error"])
		}
		return nil
	})

	step6 := demo.Step("resources/read on ui://wiki-explorer/mcp-app.html").
		Arrow("Host", "Server", "resources/read { uri: ui://wiki-explorer/mcp-app.html }").
		DashedArrow("Server", "Host", "Contents[0].Text = the iframe HTML").
		Note("Confirms the App's iframe HTML is served on the wire. Unlike `map` (which streams CesiumJS from a CDN at runtime and needs `_meta.ui.csp.connectDomains`), the Wikipedia fetch happens server-side — the iframe just renders the parsed graph. No CSP block needed.")
	common.WireRecipe(step6,
		`curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":6,"method":"resources/read","params":{"uri":"ui://wiki-explorer/mcp-app.html"}}' \
  | jq -r '.result.contents[0].text' | head -c 200`,
		`text, _ := c.ReadResource("ui://wiki-explorer/mcp-app.html")
fmt.Printf("%d bytes; first 200:\n%.200s\n", len(text), text)`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		text, err := c.ReadResource("ui://wiki-explorer/mcp-app.html")
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
		"The host → iframe handoff mechanics (`tools/call` → `resources/read` → sandboxed iframe → `postMessage`) are covered once in [The basic-host bridge dance](../README.md#the-basic-host-bridge-dance). Steps 1-6 above are the floor every MCP Apps interaction starts from.",
		"",
		"wiki-explorer's iframe sits at the **rich** end of the App-ness spectrum — it registers app-side tools the host can call back into, on top of the per-render bridge calls. The bridge calls its App SDK makes:",
		"",
		"- `app.ontoolresult` — unpacks `result.structuredContent` into `{page, links, error}` and seeds the d3-force graph. Each link becomes a node; the edge connects the source page to its first-degree neighbour.",
		"- `app.callServerTool({name: \"get-first-degree-links\", arguments: {url}})` — when the user clicks a node to expand its neighbours, the App issues this call directly (no model in the loop), then merges the new links into the visible graph.",
		"- `app.registerTool` ×4 — the iframe exposes app-side tools so a model can drive the visualisation from outside: `expandNode(url)`, `searchVisibleNodes(query)`, `getVisibleNodes()`, and a layout / view-state inspector. Each of these is a tool the bridge surfaces back through MCP-style tools/call from the host side.",
		"",
		"NO `_meta.ui.csp` needed — Wikipedia fetching happens server-side. The iframe's only network access is to the bridge.",
	)

	demo.Section("Where to look in the code",
		"- `main.go` — fixture is ~270 lines. The interesting parts: `wikiHrefRe` regex + `extractWikiLinks` dedup/filter loop (mirrors upstream's cheerio selector + filter chain), `wikiURLRe` input gate, `fetchAndExtractLinks` net/http call with the OSM-style User-Agent (Wikipedia returns 403 without it), and the handler's never-Go-error contract that maps fetch / parse failures into the nullable `error` field.",
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
