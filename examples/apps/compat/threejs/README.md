# threejs — code-as-input, WebGL App

Rung 5 on the [examples ladder](../README.md#reading-order--examples-ladder).
Two tools — one renders an interactive Three.js scene, the other
exposes documentation. First example where a tool's input is
literally a multi-line JavaScript program.

## What it shows

- **Two tools on one server.** `show_threejs_scene` carries the App
  iframe; `learn_threejs` is a plain (no-UI) tool that returns the
  documentation string. The host's tool dropdown picks up both.
- **Multi-line code as default value.** The default Three.js scene is
  a multi-line `code` string with commas — exactly the pattern that
  trips up struct-tag reflection (invopop's tag parser splits on
  commas, truncating defaults at the first one). The fixture uses
  `InputSchemaPatch` to land the default verbatim without restating
  the whole schema.
- **`Replace()` escape hatch.** The `height` field needs
  `exclusiveMinimum: 0` + `Number.MAX_SAFE_INTEGER` upper bound —
  shape the `PropertyBuilder` doesn't expose direct methods for. The
  fixture uses `PropertyBuilder.Replace(rawSchema)` for that one
  field while patching the others normally.

## Run it

```bash
make demo-app EXAMPLE=threejs-server
make inspect-app EXAMPLE=threejs-server
EXAMPLE=threejs-server make test-apps-playwright-docker
```

## Prompts to try

In MCPJam Inspector or basic-host, connect to `Three.js Server`, then
paste any of these into the chat:

```
Render a 3D scene using Three.js.
```

![Three.js App: iframe shows the default rotating cube rendered with the OrbitControls camera](screenshots/01-default-cube.png)

```
Show me a green wireframe sphere rotating slowly.
```

```
Make a Three.js scene with three colored cubes arranged in a triangle.
```

```
Render a torus knot with rainbow material that rotates on all three axes.
```

![Three.js App: iframe shows a rainbow torus knot generated from a model-supplied code string](screenshots/02-custom-torus.png)

```
How do I use OrbitControls in Three.js?
```

The first four should make the model call `show_threejs_scene` with a
generated `code` payload; the iframe renders the scene. The last one
should make the model call `learn_threejs` (no iframe — plain text
docs back).

### Direct tool call (no LLM needed)

| What | How | What you should see |
|---|---|---|
| Default scene | Select `show_threejs_scene`, call with empty input | App iframe renders the default rotating cube. Tool result: `{"success": true}` |
| Verify the multi-line default landed intact | Expand `show_threejs_scene`'s `inputSchema.properties.code.default` | The full multi-line JavaScript program — newlines and commas preserved verbatim, no struct-tag truncation. |
| Plain (no-UI) tool | Select `learn_threejs`, call with empty input | Tool result is a text block of Three.js API docs. No iframe — this tool has no `_meta.ui` block. |

## What to look at next

- [`shadertoy`](../shadertoy/README.md) (rung 5, sibling) — same
  "multi-line code as input" pattern, but for GLSL fragment shaders.
- [`pdf-server`](../pdf-server/README.md) (rung 7) — for examples
  where the iframe runs even more sophisticated logic.
- See [`main.go`](main.go) — the `InputSchemaPatch` block shows
  both `.Default(...)` chains and a `Replace(...)` escape side by
  side.
