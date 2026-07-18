package main

import (
	"encoding/json"
	"fmt"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
)

// runDemo is the shadertoy walkthrough. Acts as a scripted MCP host
// against a server started by `just serve` in another terminal.
//
// Walks five steps:
//
//  1. Connect to the fixture (initialize → session)
//  2. tools/list — inspect render-shadertoy's 6-field input surface
//     (one required-with-default fragmentShader + 5 optional buffers)
//     and the multi-line GLSL default that survives struct-tag
//     truncation via InputSchemaPatch.
//  3. tools/call render-shadertoy {} — default shader (uv ramp +
//     pulsing blue). Server returns acknowledgement; the iframe
//     compiles GLSL + renders via WebGL2.
//  4. tools/call render-shadertoy with custom GLSL — radial gradient
//     plus iMouse demo; same acknowledgement, different iframe output.
//  5. resources/read on the iframe resourceUri — sanity-check the
//     HTML is served (no CSP needed; WebGL is sandbox-internal).
//
// Each step attaches a common.WireRecipe (curl + Go) for the wire
// reproduction.
func runDemo() {
	serverURL := common.MCPServerURL()

	demo := demokit.New("shadertoy — GLSL fragment shaders over MCP Apps").
		Dir("shadertoy").
		Description("Walks the render-shadertoy round trip end-to-end as a scripted MCP client: initialize, tools/list (6-field input surface with a multi-line GLSL default), tools/call with empty + custom shaders, and resources/read on the App's iframe HTML. The fixture mirrors upstream's shadertoy-server example: ShaderToy-compatible mainImage entry point, iResolution / iTime / iMouse uniforms, optional bufferA-D multi-pass channels. Server just acknowledges; the WebGL2 compile + render happens iframe-side. Run `just serve` in another terminal first.").
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
		"Any MCP host can connect to the running server (Claude Desktop, VS Code, MCPJam, basic-host). The walkthrough below acts as a scripted host that issues the protocol calls directly through `*mcpkit/client.Client` — no LLM, no browser. The same calls drive the iframe when you run `just demo-app EXAMPLE=shadertoy` in basic-host (see the [centralized guide](../README.md#other-ways-to-test-a-fixture)).",
	)

	var c *client.Client

	step1 := demo.Step("Connect to the fixture").
		Arrow("Host", "Server", "POST /mcp — initialize").
		DashedArrow("Server", "Host", "serverInfo + capabilities + Mcp-Session-Id").
		Note("Standard MCP initialize handshake. `*client.Client.Connect()` runs initialize + notifications/initialized + stashes the session header for every subsequent call.")
	common.WireRecipe(step1,
		`SID=$(curl -si -X POST `+serverURL+`/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"curl","version":"1"}}}' \
  | awk 'tolower($1) == "mcp-session-id:" {gsub(/\r/,""); print $2}')

curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized"}'`,
		`c := client.NewClient("`+serverURL+`/mcp",
    core.ClientInfo{Name: "shadertoy-host", Version: "1.0"},
)
if err := c.Connect(); err != nil {
    log.Fatalf("connect: %v", err)
}`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		c = client.NewClient(serverURL+"/mcp",
			core.ClientInfo{Name: "shadertoy-host", Version: "1.0"},
		)
		if err := c.Connect(); err != nil {
			fmt.Printf("    ERROR: %v\n    Start the server with: just serve\n", err)
			return nil
		}
		fmt.Printf("    connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)
		return nil
	})

	step2 := demo.Step("tools/list — 6-field input surface + multi-line GLSL default").
		Arrow("Host", "Server", "tools/list").
		DashedArrow("Server", "Host", "tools[] = [render-shadertoy]").
		Note("Two things worth calling out. (1) `_meta.ui.resourceUri` (`ui://shadertoy/mcp-app.html`) tags this as an App tool — basic-host renders an iframe for it. (2) `inputSchema.properties.fragmentShader.default` is the full multi-line GLSL boilerplate (the uv-ramp + pulsing-blue mainImage). Struct-tag reflection would truncate at the first comma; mcpkit lands the default verbatim via `Prop(\"fragmentShader\").Default(defaultFragmentShader)`. The other 5 fields (`common`, `bufferA`-`bufferD`) are optional strings with description-only patches.")
	common.WireRecipe(step2,
		`curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' \
  | jq '.result.tools[] | {name, fields: (.inputSchema.properties | keys), defaultPreview: .inputSchema.properties.fragmentShader.default[0:120]}'`,
		`res, _ := c.Call("tools/list", map[string]any{})
var out struct {
    Tools []struct {
        Name        string         `+"`json:\"name\"`"+`
        InputSchema map[string]any `+"`json:\"inputSchema\"`"+`
    } `+"`json:\"tools\"`"+`
}
json.Unmarshal(res.Raw, &out)
for _, t := range out.Tools {
    props, _ := t.InputSchema["properties"].(map[string]any)
    fmt.Printf("%s fields:", t.Name)
    for k := range props { fmt.Printf(" %s", k) }
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
			fmt.Printf("    ERROR: %v\n", err)
			return nil
		}
		for _, t := range out.Tools {
			fmt.Printf("    %s\n", t.Name)
			if props, ok := t.InputSchema["properties"].(map[string]any); ok {
				keys := make([]string, 0, len(props))
				for k := range props {
					keys = append(keys, k)
				}
				fmt.Printf("      fields: %v\n", keys)
				if fs, ok := props["fragmentShader"].(map[string]any); ok {
					if def, ok := fs["default"].(string); ok {
						preview := def
						if len(preview) > 160 {
							preview = preview[:160] + "…"
						}
						fmt.Printf("      fragmentShader default (preview): %s\n", preview)
					}
				}
			}
		}
		return nil
	})

	step3 := demo.Step("tools/call render-shadertoy {} — default uv-ramp shader").
		Arrow("Host", "Server", "tools/call render-shadertoy {}").
		DashedArrow("Server", "Host", "ToolResult.content[0].text = \"Shader rendered successfully\"").
		Note("The server's job is intentionally tiny — it acknowledges the call. The actual rendering side-effect happens inside the iframe's WebGL2 context: the GLSL is compiled and linked, ShaderToy uniforms (`iResolution`, `iTime`, `iMouse`, etc.) are wired up, the shader draws a full-screen quad at 60fps. With no arguments the iframe falls back to the server-advertised `fragmentShader.default` (the uv-ramp + pulsing-blue mainImage from step 2).")
	common.WireRecipe(step3,
		`curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"render-shadertoy","arguments":{}}}' \
  | jq '.result | {content, isError}'`,
		`tr, _ := c.ToolCallFull("render-shadertoy", map[string]any{})
fmt.Printf("isError=%v, text=%q\n", tr.IsError, tr.Content[0].Text)`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		tr, err := c.ToolCallFull("render-shadertoy", map[string]any{})
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

	radialShader := `void mainImage(out vec4 fragColor, in vec2 fragCoord) {
    vec2 uv = (fragCoord - 0.5 * iResolution.xy) / iResolution.y;
    float r = length(uv);
    float wave = 0.5 + 0.5 * sin(8.0 * r - 3.0 * iTime);
    vec3 col = vec3(wave, 0.6 * wave, 0.9);
    if (iMouse.z > 0.0) {
        vec2 m = (iMouse.xy - 0.5 * iResolution.xy) / iResolution.y;
        col = mix(col, vec3(1.0, 0.4, 0.2), exp(-30.0 * length(uv - m)));
    }
    fragColor = vec4(col, 1.0);
}`
	step4 := demo.Step("tools/call render-shadertoy with custom GLSL").
		Arrow("Host", "Server", "tools/call render-shadertoy {fragmentShader: \"radial wave + iMouse highlight\"}").
		DashedArrow("Server", "Host", "ToolResult.content[0].text = \"Shader rendered successfully\"").
		Note("Same call shape, different `fragmentShader`. This walkthrough sends a radial-wave demo that also reacts to iMouse — when the user presses on the iframe (`iMouse.z > 0`), a soft orange highlight tracks the cursor. The bridge / iframe re-compile and run the new shader; the model's view of the wire result is unchanged (same acknowledgement text).")
	common.WireRecipe(step4,
		`# fragmentShader: a radial-wave demo with iMouse reactivity (full source in the Go recipe below).
curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"render-shadertoy","arguments":{"fragmentShader":"<radial-wave shader>"}}}' \
  | jq '.result.content[0].text'`,
		`tr, _ := c.ToolCallFull("render-shadertoy", map[string]any{
    "fragmentShader": radialShader, // see source above the call site
})
fmt.Printf("isError=%v, text=%q\n", tr.IsError, tr.Content[0].Text)`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		tr, err := c.ToolCallFull("render-shadertoy", map[string]any{
			"fragmentShader": radialShader,
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
		fmt.Printf("    (sent %d bytes of custom GLSL; iframe re-compiled + re-rendered)\n", len(radialShader))
		return nil
	})

	step5 := demo.Step("resources/read on ui://shadertoy/mcp-app.html").
		Arrow("Host", "Server", "resources/read { uri: ui://shadertoy/mcp-app.html }").
		DashedArrow("Server", "Host", "Contents[0].Text = the iframe HTML").
		Note("Confirms the App's iframe HTML is served on the wire. Unlike `map` (which streams CesiumJS from a CDN and needs `_meta.ui.csp.connectDomains`), shadertoy's WebGL2 is part of the browser runtime itself and the GLSL compiler ships in WebGL — no external network access needed. No CSP block.")
	common.WireRecipe(step5,
		`curl -s -X POST `+serverURL+`/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":5,"method":"resources/read","params":{"uri":"ui://shadertoy/mcp-app.html"}}' \
  | jq -r '.result.contents[0].text' | head -c 200`,
		`text, _ := c.ReadResource("ui://shadertoy/mcp-app.html")
fmt.Printf("%d bytes; first 200:\n%.200s\n", len(text), text)`,
	).Run(func(ctx demokit.StepContext) *demokit.StepResult {
		text, err := c.ReadResource("ui://shadertoy/mcp-app.html")
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
		"shadertoy's iframe sits at the **moderate** end of the App-ness spectrum. The bridge calls its App SDK makes:",
		"",
		"- `app.ontoolresult` — reads each `render-shadertoy` call's arguments (`fragmentShader` + optional `common` / `bufferA`-`D`), recompiles the shader, and resumes the WebGL2 render loop. The tool result's text content (`\"Shader rendered successfully\"`) is informational; the actual rendering side-effect lives in the GL context.",
		"- `app.getHostContext()` + `app.onhostcontextchanged` — applies host theme + safeAreaInsets to the iframe chrome.",
		"",
		"NO `app.registerTool`, NO `app.callServerTool` after the initial load. The iframe is a pure renderer: server passes shader text, iframe runs it. Same spectrum rung as `threejs` (code-as-input → bundled WebGL renderer).",
	)

	demo.Section("Where to look in the code",
		"- `main.go` — fixture is ~180 lines. Two interesting bits: (1) `defaultFragmentShader` (the multi-line GLSL boilerplate ShaderToy users expect — preserved verbatim through `Prop(\"fragmentShader\").Default(...)` since struct-tags would truncate at the first comma); (2) the rich `toolDescription` constant (ported byte-for-byte from upstream's `TOOL_DESCRIPTION` — encodes ShaderToy conventions, available uniforms, mouse-interaction protocol, multi-pass buffer wiring — so models know what to write without scraping a docs site).",
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
