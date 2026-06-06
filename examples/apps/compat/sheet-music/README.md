# sheet-music — multi-line default, first use of the patch escape

Rung 3 on the [examples ladder](../README.md#reading-order--examples-ladder).
One tool, but the input has a multi-line default value with commas —
the first fixture where reflection alone won't produce the right
schema. Introduces the `InputSchemaPatch` escape hatch.

## What it Shows

- **ABC notation input.** `play-sheet-music` accepts an ABC notation
  string and the iframe renders it as both readable sheet music and
  playable audio (via abcjs in the iframe).
- **The struct-tag-comma problem.** Upstream's default is an 11-line
  ABC notation string for "Twinkle Twinkle Little Star" — which
  contains commas. invopop's struct-tag parser would truncate the
  default at the first comma. The fixture uses `InputSchemaPatch` to
  land the default verbatim via
  `s.Prop("abcNotation").Default(defaultABCNotation)`.

## Or Run Live

### Start Server

```bash
make demo-app EXAMPLE=sheet-music
```

Starts the mcpkit-Go fixture on `http://localhost:3101/mcp` and basic-host on `http://localhost:8080`. (Pass `OPEN=1` to auto-open the browser.)

## Try It Out on basic-host

Open <http://localhost:8080> in your browser. Then:

1. Pick **Sheet Music Server** from the server dropdown.
2. Pick **play-sheet-music** from the tool dropdown, click **Call Tool**.
3. The iframe renders the result; interact with it directly to drive subsequent tool calls (no model in the loop).

<a href="screenshots/01-twinkle.png" target="_blank"><img src="screenshots/01-twinkle.png" alt="Sheet Music App rendered in basic-host: iframe shows Twinkle Twinkle as rendered sheet music with a play button" width="50%"></a>

## Try It Out from a Host

Connect to `http://localhost:3101/mcp` from your favorite MCP host — VS Code, Claude Desktop, [MCPJam Inspector](https://github.com/MCPJam/inspector), or any spec-compliant client.

**Prompts to try** (LLM-driven hosts):

> "Play Twinkle Twinkle Little Star on the sheet music tool."
> "Show me sheet music for "Mary Had a Little Lamb" in C major."
> "Use the play-sheet-music tool with the default ABC notation."
> "Render Greensleeves in the key of A minor."

The model calls `play-sheet-music` with ABC notation (recalling it or
constructing it); the iframe renders the sheet music and lets you
play it back.

**Verify the wire shape** (no LLM needed):

| What | How | What you should see |
|---|---|---|
| Default tune | Select `play-sheet-music`, call with empty input | Iframe renders Twinkle Twinkle as sheet music + audio player |
| Verify the multi-line default landed intact | Expand `inputSchema.properties.abcNotation.default` | The full 11-line ABC notation including commas — no truncation. This is what `InputSchemaPatch` preserves. |

See [Other ways to test a fixture](../README.md#other-ways-to-test-a-fixture) in the compat README for wire inspection, upstream comparison, the strict Playwright gate, and connecting from VS Code / Claude Desktop / other MCP hosts.

## What to Try Next

- [`shadertoy`](../shadertoy/README.md) — same "multi-line code as
  input" pattern for GLSL.
- [`threejs`](../threejs/README.md) — same again, but also introduces
  `PropertyBuilder.Replace()` for fields the typed builder doesn't
  cover.
- [`scenario-modeler`](../scenario-modeler/README.md) — rung 4, takes
  the schema-override pattern to nested nullable fields.
