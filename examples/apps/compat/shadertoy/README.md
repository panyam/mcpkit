# shadertoy — GLSL fragment shader, WebGL App

Rung 5 on the [examples ladder](../README.md#reading-order--examples-ladder).
One tool, but the input is a GLSL fragment shader program. First
fixture targeted at WebGL-style rendering inside the App iframe.

## What it Shows

- **GLSL as input.** `render-shadertoy` accepts a `fragmentShader`
  field (plus optional `common` and four buffer channels for
  multi-pass rendering) and runs it in the iframe via WebGL 2.0,
  using ShaderToy conventions (`iTime`, `iResolution`, `iChannel*`).
- **Multi-line default with commas.** The default fragment shader is
  several lines of GLSL containing commas — struct-tag reflection
  would truncate at the first comma. The fixture uses
  `InputSchemaPatch` to land the multi-line default verbatim AND
  add descriptions for each of the six optional channel fields.
- **Six fields patched in one chain.** Showcases the
  `s.Prop(name).Desc(...)` pattern across multiple properties — much
  shorter than the equivalent override map.

## Or Run Live

### Start Server

```bash
make demo-app EXAMPLE=shadertoy
```

Starts the mcpkit-Go fixture on `http://localhost:3101/mcp` and basic-host on `http://localhost:8080`. (Pass `OPEN=1` to auto-open the browser.)

## Try It Out on basic-host

Open <http://localhost:8080> in your browser. Then:

1. Pick **ShaderToy Server** from the server dropdown.
2. Pick **render-shadertoy** from the tool dropdown, click **Call Tool**.
3. The iframe renders the result; interact with it directly to drive subsequent tool calls (no model in the loop).

<a href="screenshots/01-default-shader.png" target="_blank"><img src="screenshots/01-default-shader.png" alt="ShaderToy App: iframe shows the default UV gradient shader running in WebGL" width="50%"></a>

## Try It Out from a Host

Connect to `http://localhost:3101/mcp` from your favorite MCP host — VS Code, Claude Desktop, [MCPJam Inspector](https://github.com/MCPJam/inspector), or any spec-compliant client.

**Prompts to try** (LLM-driven hosts):

> "Render a ShaderToy shader."
> "Show me a shader that displays rainbow colors that shift over time."
> "Render a Mandelbrot fractal as a fragment shader."
> "Show me a plasma effect using sin and cos of iTime."
> "Render a shader that draws concentric pulsing circles centered at the screen."

The model calls `render-shadertoy` with the generated GLSL; the
iframe compiles and runs it on the GPU.

**Verify the wire shape** (no LLM needed):

| What | How | What you should see |
|---|---|---|
| Default shader | Select `render-shadertoy`, call with empty input | Iframe renders the default UV gradient shader (the default `fragmentShader` value) |
| Verify the multi-line default landed intact | Expand `inputSchema.properties.fragmentShader.default` | The full multi-line GLSL with commas preserved — what `InputSchemaPatch` guarantees |
| Custom shader | Call with a `{"fragmentShader": "void mainImage(...)"}` payload | Iframe renders whatever GLSL you supplied (errors land in the iframe's console) |

See [Other ways to test a fixture](../README.md#other-ways-to-test-a-fixture) in the compat README for wire inspection, upstream comparison, the strict Playwright gate, and connecting from VS Code / Claude Desktop / other MCP hosts.

## What to Try Next

- [`threejs`](../threejs/README.md) — rung-5 sibling; also takes code
  as input, but for Three.js scene setup instead of fragment shaders.
- [`pdf-server`](../pdf-server/README.md) — rung-7 endgame for "the
  iframe runs sophisticated logic" pattern.
