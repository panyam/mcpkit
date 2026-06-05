# sheet-music — multi-line default, first use of the patch escape

Rung 3 on the [examples ladder](../README.md#reading-order--examples-ladder).
One tool, but the input has a multi-line default value with commas —
the first fixture where reflection alone won't produce the right
schema. Introduces the `InputSchemaPatch` escape hatch.

## What it shows

- **ABC notation input.** `play-sheet-music` accepts an ABC notation
  string and the iframe renders it as both readable sheet music and
  playable audio (via abcjs in the iframe).
- **The struct-tag-comma problem.** Upstream's default is an 11-line
  ABC notation string for "Twinkle Twinkle Little Star" — which
  contains commas. invopop's struct-tag parser would truncate the
  default at the first comma. The fixture uses `InputSchemaPatch` to
  land the default verbatim via
  `s.Prop("abcNotation").Default(defaultABCNotation)`.

## Run it

Boots the mcpkit-Go fixture (`main.go` in this folder) and opens
[MCPJam Inspector](https://github.com/MCPJam/inspector) so you can poke
at the protocol surface:

```bash
make demo-app EXAMPLE=sheet-music
```

Paste `http://localhost:3101/mcp` into MCPJam's server list and connect.
Then browse `tools/list`, `_meta.ui`, and tool-call payloads on the wire.

See [Other ways to test a fixture](../README.md#other-ways-to-test-a-fixture) in the compat README for wire inspection, upstream comparison, and the strict Playwright gate.

## Prompts to try

Connect to `Sheet Music Server`, then paste any of these:

```
Play Twinkle Twinkle Little Star on the sheet music tool.
```

![Sheet Music App rendered in basic-host: iframe shows Twinkle Twinkle as rendered sheet music with a play button](screenshots/01-twinkle.png)

```
Show me sheet music for "Mary Had a Little Lamb" in C major.
```

```
Use the play-sheet-music tool with the default ABC notation.
```

```
Render Greensleeves in the key of A minor.
```

![Sheet music for Greensleeves in A minor with audio playback controls](screenshots/02-greensleeves.png)

The model calls `play-sheet-music` with ABC notation (recalling it or
constructing it); the iframe renders the sheet music and lets you
play it back.

### Direct tool call (no LLM needed)

| What | How | What you should see |
|---|---|---|
| Default tune | Select `play-sheet-music`, call with empty input | Iframe renders Twinkle Twinkle as sheet music + audio player |
| Verify the multi-line default landed intact | Expand `inputSchema.properties.abcNotation.default` | The full 11-line ABC notation including commas — no truncation. This is what `InputSchemaPatch` preserves. |

## What to look at next

- [`shadertoy`](../shadertoy/README.md) — same "multi-line code as
  input" pattern for GLSL.
- [`threejs`](../threejs/README.md) — same again, but also introduces
  `PropertyBuilder.Replace()` for fields the typed builder doesn't
  cover.
- [`scenario-modeler`](../scenario-modeler/README.md) — rung 4, takes
  the schema-override pattern to nested nullable fields.
