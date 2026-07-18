package main

import (
	"encoding/json"
	"fmt"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
)

// runDemo is the threejs walkthrough. Acts as a scripted MCP host
// against a server started by `just serve` in another terminal.
//
// Walks six steps:
//
//  1. Connect to the fixture (initialize → session)
//  2. tools/list — verify show_threejs_scene (App) + learn_threejs
//     (plain); call out the per-prop schema patch (`code` default
//     is multi-line + comma-rich, `height` uses an explicit Replace
//     for the upstream exclusiveMinimum + MAX_SAFE_INTEGER bounds)
//  3. tools/call show_threejs_scene {} — default Three.js code:
//     server returns success:true, the iframe runs the code in a
//     sandboxed Web Worker and renders the rotating cube
//  4. tools/call show_threejs_scene with custom code — the model /
//     basic-host user can send any Three.js snippet and the iframe
//     re-runs it (sphere + OrbitControls in this walkthrough)
//  5. tools/call learn_threejs {} — full multi-page markdown doc
//     that lists the available globals + 3 ready-to-run examples.
//     This is what makes the App self-describing for a model.
//  6. resources/read on the iframe resourceUri — verify the bundled
//     iframe HTML (Three.js is embedded, no CDN needed)
//
// Each step attaches a common.WireRecipe (curl + Go) for the wire
// reproduction.
func runDemo() {
	serverURL := common.MCPServerURL()

	demo := demokit.New("threejs — Three.js scenes from arbitrary JS over MCP Apps").
		Dir("threejs").
		Description("Walks the show_threejs_scene + learn_threejs round trip end-to-end as a scripted MCP client: initialize, tools/list, default + custom scene calls, the full markdown documentation tool, and resources/read on the iframe. The fixture mirrors upstream's threejs-server example: arbitrary Three.js code runs sandboxed in a Web Worker inside the iframe, available globals (THREE, OrbitControls, EffectComposer, etc.) bundled with the iframe (no CDN). Run `just serve` in another terminal first.").
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
		"Any MCP host can connect to the running server (Claude Desktop, VS Code, MCPJam, basic-host). The walkthrough below acts as a scripted host that issues the protocol calls directly through `*mcpkit/client.Client` — no LLM, no browser. The same calls drive the iframe when you run `just demo-app EXAMPLE=threejs` in basic-host (see the [centralized guide](../README.md#other-ways-to-test-a-fixture)).",
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
    core.ClientInfo{Name: "threejs-host", Version: "1.0"},
)
if err := c.Connect(); err != nil {
    log.Fatalf("connect: %v", err)
}`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		c = client.NewClient(serverURL+"/mcp",
			core.ClientInfo{Name: "threejs-host", Version: "1.0"},
		)
		if err := c.Connect(); err != nil {
			fmt.Printf("    ERROR: %v\n    Start the server with: just serve\n", err)
			return nil
		}
		fmt.Printf("    connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)
		return nil
	})

	step2 := demo.Step("tools/list — show_threejs_scene + learn_threejs").
		Arrow("Host", "Server", "tools/list").
		DashedArrow("Server", "Host", "tools[] = [show_threejs_scene, learn_threejs]").
		Note("Two distinct shapes side-by-side. `show_threejs_scene` carries `_meta.ui.resourceUri` → basic-host renders an iframe; `learn_threejs` has no `_meta.ui` → plain tool. Two notable schema details in `show_threejs_scene.inputSchema`: (1) `code.default` is upstream's full multi-line rotating-cube snippet (commas + newlines preserved via `Prop().Default(...)`, not struct-tags which truncate at the first comma); (2) `height` uses an explicit `Replace` with `exclusiveMinimum:0` + `maximum:9007199254740991` (Number.MAX_SAFE_INTEGER) — upstream's `z.number().int().positive()` shape doesn't fit reflection.")
	common.WireRecipe(step2,
		`curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' \
  | jq '.result.tools[] | {name, hasUI: (._meta.ui != null), heightSchema: .inputSchema.properties.height}'`,
		`res, _ := c.Call("tools/list", map[string]any{})
var out struct {
    Tools []struct {
        Name        string         `+"`json:\"name\"`"+`
        InputSchema map[string]any `+"`json:\"inputSchema\"`"+`
        Meta        map[string]any `+"`json:\"_meta,omitempty\"`"+`
    } `+"`json:\"tools\"`"+`
}
json.Unmarshal(res.Raw, &out)
for _, t := range out.Tools {
    fmt.Printf("  %s  hasUI=%v\n", t.Name, t.Meta["ui"] != nil)
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
				Meta        map[string]any `json:"_meta,omitempty"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(res.Raw, &out); err != nil {
			fmt.Printf("    ERROR decoding tools/list: %v\n", err)
			return nil
		}
		for _, t := range out.Tools {
			hasUI := t.Meta != nil && t.Meta["ui"] != nil
			fmt.Printf("    %s  hasUI=%v\n", t.Name, hasUI)
			if props, ok := t.InputSchema["properties"].(map[string]any); ok {
				if h, ok := props["height"].(map[string]any); ok {
					hb, _ := json.MarshalIndent(h, "      ", "  ")
					fmt.Printf("      height schema: %s\n", string(hb))
				}
			}
		}
		return nil
	})

	step3 := demo.Step("tools/call show_threejs_scene {} — default rotating cube").
		Arrow("Host", "Server", "tools/call show_threejs_scene {}").
		DashedArrow("Server", "Host", "structuredContent = { success: true }").
		Note("Server runs no Three.js code itself — it just acknowledges the call. The iframe's sandboxed Web Worker reads the latest `show_threejs_scene` argument from the bridge and `eval`s the JavaScript with `THREE`, `canvas`, `width`, `height`, etc. in scope. With no arguments the iframe falls back to the server-advertised `code` default (the rotating cube — also visible in `tools/list.inputSchema.properties.code.default`).")
	common.WireRecipe(step3,
		`curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"show_threejs_scene","arguments":{}}}' \
  | jq '.result | {structuredContent, isError}'`,
		`tr, _ := c.ToolCallFull("show_threejs_scene", map[string]any{})
fmt.Printf("structuredContent: %v, isError: %v\n", tr.StructuredContent, tr.IsError)`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		tr, err := c.ToolCallFull("show_threejs_scene", map[string]any{})
		if err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		fmt.Printf("    structuredContent: %v, isError: %v\n", tr.StructuredContent, tr.IsError)
		return nil
	})

	step4 := demo.Step("tools/call show_threejs_scene with custom Three.js code").
		Arrow("Host", "Server", "tools/call show_threejs_scene {code: \"OrbitControls sphere\", height: 500}").
		DashedArrow("Server", "Host", "structuredContent = { success: true }").
		Note("The interesting path — a model (or a basic-host user) sends arbitrary Three.js code and the iframe re-runs it. This step uses an OrbitControls sphere example from `learn_threejs`'s documentation (Step 5). Same call shape as Step 3, just different `code` and a custom `height`.")
	sampleCode := `const scene = new THREE.Scene();
const camera = new THREE.PerspectiveCamera(75, width / height, 0.1, 1000);
const renderer = new THREE.WebGLRenderer({ canvas, antialias: true, alpha: true });
renderer.setSize(width, height);
renderer.setClearColor(0x000000, 0);

const controls = new OrbitControls(camera, renderer.domElement);
controls.enableDamping = true;

const sphere = new THREE.Mesh(
  new THREE.SphereGeometry(1, 32, 32),
  new THREE.MeshStandardMaterial({ color: 0xff6b6b, roughness: 0.4 })
);
scene.add(sphere);

scene.add(new THREE.DirectionalLight(0xffffff, 1));
scene.add(new THREE.AmbientLight(0x404040));

camera.position.z = 4;

function animate() {
  requestAnimationFrame(animate);
  controls.update();
  renderer.render(scene, camera);
}
animate();`
	common.WireRecipe(step4,
		`# code: see learn_threejs "Example: Interactive OrbitControls" for the full snippet.
curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"show_threejs_scene","arguments":{"code":"<orbit-controls sphere snippet>","height":500}}}' \
  | jq '.result | {structuredContent, isError}'`,
		`tr, _ := c.ToolCallFull("show_threejs_scene", map[string]any{
    "code":   orbitControlsSphere,  // see learn_threejs docs
    "height": 500,
})
fmt.Printf("structuredContent: %v\n", tr.StructuredContent)`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		tr, err := c.ToolCallFull("show_threejs_scene", map[string]any{
			"code":   sampleCode,
			"height": 500,
		})
		if err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		fmt.Printf("    structuredContent: %v, isError: %v\n", tr.StructuredContent, tr.IsError)
		fmt.Printf("    (sent %d bytes of Three.js code, height=500)\n", len(sampleCode))
		return nil
	})

	step5 := demo.Step("tools/call learn_threejs — full markdown documentation").
		Arrow("Host", "Server", "tools/call learn_threejs {}").
		DashedArrow("Server", "Host", "ToolResult.content[0].text = ~2.7KB of markdown").
		Note("The self-documenting half of the App. Returns a multi-page markdown reference: available globals (`THREE`, `OrbitControls`, `EffectComposer`, etc.), the transparent-background convention (`alpha: true` + `setClearColor(0x000000, 0)`), three worked examples (basic template, rotating cube, OrbitControls sphere), and tips. A model that hasn't seen this fixture before can call `learn_threejs` once and then write a working scene without scraping a docs site.")
	common.WireRecipe(step5,
		`curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"learn_threejs","arguments":{}}}' \
  | jq -r '.result.content[0].text' | head -20`,
		`tr, _ := c.ToolCallFull("learn_threejs", map[string]any{})
fmt.Printf("%d bytes of docs:\n%s\n", len(tr.Content[0].Text), tr.Content[0].Text[:200])`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		tr, err := c.ToolCallFull("learn_threejs", map[string]any{})
		if err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		text := ""
		if len(tr.Content) > 0 {
			text = tr.Content[0].Text
		}
		fmt.Printf("    %d bytes of documentation\n", len(text))
		preview := text
		if len(preview) > 350 {
			preview = preview[:350] + "…"
		}
		fmt.Printf("    %s\n", preview)
		return nil
	})

	step6 := demo.Step("resources/read on ui://threejs/mcp-app.html").
		Arrow("Host", "Server", "resources/read { uri: ui://threejs/mcp-app.html }").
		DashedArrow("Server", "Host", "Contents[0].Text = the bundled iframe HTML").
		Note("Confirms the App's iframe HTML is actually served on the MCP wire. basic-host fetches the same URI once it sees `_meta.ui.resourceUri`. Unlike `map` (which streams CesiumJS from a CDN at runtime and needs `_meta.ui.csp.connectDomains`), `threejs` bundles the Three.js library + OrbitControls + post-processing passes directly into the iframe — the resource read is ~1.3MB of HTML/JS but no runtime network access needed. So no CSP block here.")
	common.WireRecipe(step6,
		`curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":6,"method":"resources/read","params":{"uri":"ui://threejs/mcp-app.html"}}' \
  | jq -r '.result.contents[0].text' | head -c 200`,
		`text, _ := c.ReadResource("ui://threejs/mcp-app.html")
fmt.Printf("%d bytes total; first 200:\n%.200s\n", len(text), text)`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		text, err := c.ReadResource("ui://threejs/mcp-app.html")
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
		"threejs's iframe sits at the **moderate** end of the App-ness spectrum. The bridge calls its App SDK makes:",
		"",
		"- `app.ontoolresult` — reads each `show_threejs_scene` call's arguments (`code` + `height`) and re-runs the JS inside a sandboxed Web Worker. The `structuredContent.success` is informational; the actual rendering side-effect happens iframe-side.",
		"- `app.getHostContext()` + `app.onhostcontextchanged` — applies host theme so the iframe's chrome matches basic-host's dark/light mode.",
		"",
		"NO `app.registerTool`, NO `app.updateModelContext`, NO `app.callServerTool` — the iframe is a pure renderer. The server's role reduces to: hand the iframe HTML over once, then proxy show_threejs_scene calls so the iframe sees the latest code. `learn_threejs` is the documentation channel — a model can call it once to discover the available globals and then write valid show_threejs_scene calls.",
		"",
		"**Why bundling matters.** Unlike `map` which streams CesiumJS from `cesium.com` CDN at runtime (and needs `_meta.ui.csp.connectDomains` to make it work), `threejs` bundles Three.js + all the post-processing passes into the iframe HTML at build time. The trade-off: the resource read is ~1.3MB (vs map's ~340KB), but there's zero runtime CSP / network surface to manage.",
	)

	demo.Section("Where to look in the code",
		"- `main.go` — fixture is ~250 lines. Three interesting bits: (1) the ported `threeJSDocumentation` block (~75 lines of markdown matching upstream's `THREEJS_DOCUMENTATION` byte-for-byte); (2) `InputSchemaPatch` on `show_threejs_scene` for both `code.default` (multi-line + comma-rich, struct-tags would truncate) and `height` (explicit `Replace` for the `exclusiveMinimum`+`MAX_SAFE_INTEGER` cap); (3) the second `learn_threejs` tool registered as a plain `core.TypedTool` (no UI metadata).",
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
