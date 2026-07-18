package main

import (
	"encoding/json"
	"fmt"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
)

// runDemo is the map walkthrough. Acts as a scripted MCP host against
// a server started by `just serve` in another terminal.
//
// Walks six steps:
//
//  1. Connect to the fixture (initialize → session)
//  2. tools/list — verify both show-map (App tool) + geocode (plain
//     MCP tool, no UI). Call out _meta.ui.resourceUri on show-map and
//     execution.taskSupport: "forbidden" on both.
//  3. resources/read on the iframe resourceUri — verify _meta.ui.csp
//     carries cesium.com / *.openstreetmap.org allowlists. The CSP
//     fix is the headline change in this walkthrough — without it,
//     basic-host's default CSP blocks CesiumJS from loading.
//  4. tools/call show-map {} — sync stub response ("Displaying globe.").
//     The iframe renders the globe client-side; the tool result is
//     intentionally tiny.
//  5. tools/call geocode {query: "Eiffel Tower"} — real round trip to
//     Nominatim OSM. Rate-limited server-side to 1.1s/request per
//     OSM's policy. Returns up to 5 matches with bounding boxes.
//  6. tools/call show-map {west:...,south:...,east:...,north:...,
//     label:"Eiffel Tower"} — the typical geocode → show-map chain
//     a model or basic-host user would run.
//
// Each step attaches a common.WireRecipe (curl + Go) for the wire
// reproduction.
func runDemo() {
	serverURL := common.MCPServerURL()

	demo := demokit.New("map — CesiumJS globe over MCP Apps").
		Dir("map").
		Description("Walks the show-map + geocode round trip end-to-end as a scripted MCP client: initialize, tools/list, resources/read (CSP allowlist verified), tools/call show-map, tools/call geocode (live OSM Nominatim hit), and the chained geocode → show-map call a model would make. The fixture mirrors upstream's map-server example: CesiumJS-backed iframe, per-content `_meta.ui.csp.connectDomains` + `resourceDomains` for cesium.com and *.openstreetmap.org, OSM Nominatim geocoding with 1.1s rate-limit. Run `just serve` in another terminal first.").
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
		"Any MCP host can connect to the running server (Claude Desktop, VS Code, MCPJam, basic-host). The walkthrough below acts as a scripted host that issues the protocol calls directly through `*mcpkit/client.Client` — no LLM, no browser. The same calls drive the iframe when you run `just demo-app EXAMPLE=map` in basic-host (see the [centralized guide](../README.md#other-ways-to-test-a-fixture)).",
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
    core.ClientInfo{Name: "map-host", Version: "1.0"},
)
if err := c.Connect(); err != nil {
    log.Fatalf("connect: %v", err)
}`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		c = client.NewClient(serverURL+"/mcp",
			core.ClientInfo{Name: "map-host", Version: "1.0"},
		)
		if err := c.Connect(); err != nil {
			fmt.Printf("    ERROR: %v\n    Start the server with: just serve\n", err)
			return nil
		}
		fmt.Printf("    connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)
		return nil
	})

	step2 := demo.Step("tools/list — show-map (App) + geocode (plain)").
		Arrow("Host", "Server", "tools/list").
		DashedArrow("Server", "Host", "tools[] = [show-map, geocode]").
		Note("Two distinct shapes side-by-side. `show-map` carries `_meta.ui.resourceUri` (`ui://cesium-map/mcp-app.html`) → basic-host renders an iframe for it. `geocode` has NO `_meta.ui` → plain MCP tool, no UI. Both declare `execution.taskSupport: \"forbidden\"` (sync-only — can't be wrapped in an MCP Task). The model / basic-host typically calls geocode first to resolve a place name into a bounding box, then calls show-map with that bounding box.")
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

	step3 := demo.Step("resources/read — verify _meta.ui.csp on the resource content").
		Arrow("Host", "Server", "resources/read { uri: ui://cesium-map/mcp-app.html }").
		DashedArrow("Server", "Host", "Contents[0] = { Text, _meta.ui.csp.connectDomains + resourceDomains }").
		Note("Headline fix in this walkthrough. CesiumJS streams its viewer from cesium.com CDN, and OSM tiles + Nominatim geocoding hit *.openstreetmap.org. Without per-content `_meta.ui.csp.connectDomains` + `resourceDomains`, basic-host's default CSP blocks both — the iframe loads but Cesium fails with \"Failed to load CesiumJS from CDN\". mcpkit attaches `core.ResourceContentMeta{UI:{CSP: ...}}` to the iframe's `ResourceReadContent` so basic-host sees the allowlist on the preferred path (content-level beats listing-level per the spec). Same mechanism `sheet-music` uses for paulrosen.github.io soundfonts.")
	common.WireRecipe(step3,
		`curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":3,"method":"resources/read","params":{"uri":"ui://cesium-map/mcp-app.html"}}' \
  | jq '.result.contents[0]._meta'`,
		`tr, _ := c.ReadResourceFull("ui://cesium-map/mcp-app.html")
// tr.Contents[0].Meta.UI.CSP carries connectDomains + resourceDomains.
pretty, _ := json.MarshalIndent(tr.Contents[0].Meta, "", "  ")
fmt.Println(string(pretty))`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		full, err := c.ReadResourceFull("ui://cesium-map/mcp-app.html")
		if err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		if len(full.Contents) == 0 {
			fmt.Printf("    no contents\n")
			return nil
		}
		c0 := full.Contents[0]
		fmt.Printf("    %d bytes of HTML, mimeType: %s\n", len(c0.Text), c0.MimeType)
		meta, _ := json.MarshalIndent(c0.Meta, "    ", "  ")
		fmt.Printf("    _meta: %s\n", string(meta))
		return nil
	})

	step4 := demo.Step("tools/call show-map {} — sync stub response").
		Arrow("Host", "Server", "tools/call show-map {}").
		DashedArrow("Server", "Host", "ToolResult.content[0].text = \"Displaying globe.\"").
		Note("show-map's tool result is intentionally tiny — `\"Displaying globe.\"`. The iframe loads its own CesiumJS bundle and renders the globe entirely client-side; the model just needs to know the call succeeded. With no arguments the server uses the default London bounding box from the struct-tag defaults (west=-0.5, south=51.3, east=0.3, north=51.7).")
	common.WireRecipe(step4,
		`curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"show-map","arguments":{}}}' \
  | jq '.result | {content, isError}'`,
		`tr, _ := c.ToolCallFull("show-map", map[string]any{})
fmt.Printf("isError=%v, text=%q\n", tr.IsError, tr.Content[0].Text)`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		tr, err := c.ToolCallFull("show-map", map[string]any{})
		if err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		text := ""
		if len(tr.Content) > 0 {
			text = tr.Content[0].Text
		}
		fmt.Printf("    isError=%v, text=%q\n", tr.IsError, text)
		return nil
	})

	step5 := demo.Step("tools/call geocode {query: \"Eiffel Tower\"} — live OSM round trip").
		Arrow("Host", "Server", "tools/call geocode {query: \"Eiffel Tower\"}").
		DashedArrow("Server", "Host", "ToolResult.content[0].text = formatted 1-5 results").
		Note("Real network round trip — the server hits `https://nominatim.openstreetmap.org/search`, honouring OSM's 1 request/sec usage policy (1.1s rate-limit window, single sync.Mutex serialising calls). User-Agent is required by Nominatim's policy. The first match's bounding box from this result is what step 6 feeds into show-map.")
	common.WireRecipe(step5,
		`curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"geocode","arguments":{"query":"Eiffel Tower"}}}' \
  | jq -r '.result.content[0].text'`,
		`tr, _ := c.ToolCallFull("geocode", map[string]any{"query": "Eiffel Tower"})
fmt.Println(tr.Content[0].Text)`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		tr, err := c.ToolCallFull("geocode", map[string]any{"query": "Eiffel Tower"})
		if err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		text := ""
		if len(tr.Content) > 0 {
			text = tr.Content[0].Text
		}
		if tr.IsError {
			fmt.Printf("    isError: %s\n", text)
			return nil
		}
		// Geocode results can be long; preview first 400 chars.
		preview := text
		if len(preview) > 400 {
			preview = preview[:400] + "…"
		}
		fmt.Printf("    %s\n", preview)
		return nil
	})

	step6 := demo.Step("tools/call show-map {bounding box from geocode}").
		Arrow("Host", "Server", "tools/call show-map {west:..., south:..., east:..., north:..., label:\"Eiffel Tower\"}").
		DashedArrow("Server", "Host", "ToolResult.content[0].text = \"Displaying globe.\"").
		Note("The typical chain: geocode resolves the name → bounding box, then show-map flies the iframe's camera to that box. The iframe consumes the latest show-map call's `_meta.ui.resourceUri` arguments via the App SDK (`app.ontoolresult`) and re-frames the globe. This step uses the Eiffel Tower's canonical bounding box rather than parsing step 5's text output — keeps the trace reproducible even if Nominatim's importance-ordering shuffles the top result.")
	common.WireRecipe(step6,
		`curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"show-map","arguments":{"west":2.293,"south":48.855,"east":2.297,"north":48.860,"label":"Eiffel Tower"}}}' \
  | jq '.result.content[0]'`,
		`tr, _ := c.ToolCallFull("show-map", map[string]any{
    "west":  2.293,
    "south": 48.855,
    "east":  2.297,
    "north": 48.860,
    "label": "Eiffel Tower",
})
fmt.Printf("isError=%v, text=%q\n", tr.IsError, tr.Content[0].Text)`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		tr, err := c.ToolCallFull("show-map", map[string]any{
			"west":  2.293,
			"south": 48.855,
			"east":  2.297,
			"north": 48.860,
			"label": "Eiffel Tower",
		})
		if err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		text := ""
		if len(tr.Content) > 0 {
			text = tr.Content[0].Text
		}
		fmt.Printf("    isError=%v, text=%q\n", tr.IsError, text)
		return nil
	})

	demo.Section("What the App iframe does with all this",
		"The host → iframe handoff mechanics (`tools/call` → `resources/read` → sandboxed iframe → `postMessage`) are covered once in [The basic-host bridge dance](../README.md#the-basic-host-bridge-dance). Steps 1-6 above are the floor every MCP Apps interaction starts from, plus the CSP allowlist step 3 surfaces.",
		"",
		"map's iframe sits at the **moderate** end of the App-ness spectrum. The bridge calls its App SDK makes:",
		"",
		"- `app.ontoolresult` — reads each show-map tool result and re-frames the CesiumJS camera to the new bounding box. The tool result's text content (`\"Displaying globe.\"`) is ignored; the `_meta.ui.resourceUri` arguments carry the navigation instructions.",
		"- `app.callServerTool({name: \"geocode\", arguments: {query: ...}})` — when a user types in the iframe's search box, the App calls the server's geocode tool via the bridge (no LLM in the loop), picks the top result, and chains a self-issued show-map call.",
		"- `app.getHostContext()` + `app.onhostcontextchanged` — applies host theme / safeAreaInsets to the iframe's chrome.",
		"",
		"NO `app.registerTool`, NO `app.updateModelContext` — the iframe doesn't expose tools back to the host, doesn't push context updates. Same bridge surface as `customer-segmentation` and `scenario-modeler`; one rung below the rich `budget-allocator` patterns that round-trip tool registrations back through the bridge.",
		"",
		"**Why the CSP fix matters here specifically.** Most fixtures' iframes are self-contained — their HTML + CSS + JS all live in the resource read response. map is the first compat fixture whose iframe streams a meaningful chunk of code (CesiumJS) from a CDN at runtime, AND issues runtime XHRs to a different domain (OSM Nominatim, OSM tile servers). Without per-content `_meta.ui.csp.connectDomains` + `resourceDomains`, basic-host's default CSP blocks the CDN fetch entirely.",
	)

	demo.Section("Where to look in the code",
		"- `main.go` — fixture is ~250 lines. Two tools (show-map App tool + geocode plain tool), one resource with per-content CSP _meta, and the ported `geocodeWithNominatim` (1.1s rate-limited HTTP call) + `formatGeocodeResults` text formatter that mirror upstream's server.ts.",
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
