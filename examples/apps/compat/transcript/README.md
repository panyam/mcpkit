# transcript — speech-to-text-style transcribe tool

Rung 3 on the [examples ladder](../README.md#reading-order--examples-ladder).
One tool, structured output. First fixture where the tool does
meaningful work in the iframe (rather than just rendering a value).

## What it Shows

- **Single tool with richer payload.** `transcribe` accepts an audio
  source and returns a structured transcript. The iframe renders
  the transcript with timing/segment data.
- **Typed Go output struct.** Reflection produces the JSON Schema
  from the struct alone — no override needed. A clean middle ground
  between rung 1's trivial output and rung 4's nullable / nested
  shapes.
- **Iframe Permission-Policy declaration.** First fixture to set
  `_meta.ui.permissions` on the resource read response — declares the
  Web Speech API (`microphone`) and the copy-transcript button
  (`clipboardWrite`) so basic-host (and any spec-compliant host)
  passes the right `<iframe allow=...>` attribute through to the
  sandbox. Without this declaration, `recognition.start()` silently
  fails inside the iframe — the browser blocks mic access by default.
  See [The iframe permission contract](#the-iframe-permission-contract)
  below for the wire shape and the spec-conformance footnote.

## Or Run Live

### Start Server

```bash
make demo-app EXAMPLE=transcript
```

Starts the mcpkit-Go fixture on `http://localhost:3101/mcp` and basic-host on `http://localhost:8080`. (Pass `OPEN=1` to auto-open the browser.)

## Try It Out on basic-host

Open <http://localhost:8080> in your browser. Then:

1. Pick **Transcript Server** from the server dropdown.
2. Pick **transcribe** from the tool dropdown, click **Call Tool**.
3. The iframe renders the result; interact with it directly to drive subsequent tool calls (no model in the loop).

<a href="screenshots/01-transcript-view.png" target="_blank"><img src="screenshots/01-transcript-view.png" alt="Transcript Server App rendered in basic-host: iframe shows the structured transcript with segments and timing" width="50%"></a>

## Try It Out from a Host

Connect to `http://localhost:3101/mcp` from your favorite MCP host — VS Code, Claude Desktop, [MCPJam Inspector](https://github.com/MCPJam/inspector), or any spec-compliant client.

**Prompts to try** (LLM-driven hosts):

> "Transcribe the audio at <some-audio-url>."
> "Use the transcribe tool to convert this audio to text."
> "Get me a transcript with timing segments."

The model calls `transcribe`; the iframe renders the result as a
structured transcript view.

**Verify the wire shape** (no LLM needed):

| What | How | What you should see |
|---|---|---|
| Smoke test | Select `transcribe`, call with the example input | Tool result panel shows the transcript in `structuredContent` |
| Iframe renders the transcript | Same call, scroll up | App iframe lays out the transcript with segments |

See [Other ways to test a fixture](../README.md#other-ways-to-test-a-fixture) in the compat README for wire inspection, upstream comparison, the strict Playwright gate, and connecting from VS Code / Claude Desktop / other MCP hosts.

## The iframe permission contract

The transcript App calls the browser's Web Speech API (`recognition.start()`),
which requires the host to grant the `microphone` Permission-Policy on the
sandbox iframe. The mcpkit-Go fixture declares this on the resource's
per-content `_meta.ui.permissions`:

```go
return core.ResourceResult{Contents: []core.ResourceReadContent{{
    URI:      req.URI,
    MimeType: core.AppMIMEType,
    Text:     html,
    Meta: &core.ResourceContentMeta{
        UI: &core.UIMetadata{
            Permissions: &core.UIPermissions{
                Microphone:     &struct{}{},
                ClipboardWrite: &struct{}{},
            },
        },
    },
}}}, nil
```

On the wire (`resources/read` response):

```json
"_meta": {
  "ui": {
    "permissions": { "microphone": {}, "clipboardWrite": {} }
  }
}
```

basic-host reads this object and propagates each key into the iframe's
`allow=` attribute (`microphone; clipboard-write`). The browser then prompts
for mic access on the first `recognition.start()` call. Without the `_meta`
block, the iframe loads with no policy grant and recognition silently fails
with no prompt.

### Why an object (and not an array)?

The spec defines `McpUiResourcePermissions` as a named-and-typed interface
where each value is an empty object `{}`:

```ts
interface McpUiResourcePermissions {
  camera?: {};
  microphone?: {};
  geolocation?: {};
  clipboardWrite?: {};
}
```

The empty-object values are a placeholder — future revisions can add
per-permission options without a wire break (e.g. `microphone: { autoGain: true }`).
mcpkit mirrors this with the `core.UIPermissions` struct (pointer-to-`struct{}`
fields, JSON-marshalled to the object form). Pre-`v0.3.x`, mcpkit serialized
permissions as a JSON array of strings — basic-host did property lookups on
that array and treated every permission as absent. The shape fix landed
alongside this fixture's `_meta` wiring.

### Spec-conformance footnote

The MCP Apps spec also says permissions belong **only on the UI resource**,
not on tool `_meta` (`permissions?: never` on `McpUiToolMeta`). The
`AppToolConfig.Permissions` / `TypedAppToolConfig.Permissions` fields in
`ext/ui` currently flow into tool `_meta.ui` for backward compatibility,
but basic-host does not read them from there. To make a permission take
effect in the iframe, you must set it on the **resource's** per-content
`_meta.ui` as shown above. A follow-up will deprecate the tool-meta path.

## What to Try Next

- [`sheet-music`](../sheet-music/README.md) — rung-3 sibling; the
  first place a multi-line default value trips struct-tag reflection.
- [`budget-allocator`](../budget-allocator/README.md) — rung 4, takes
  the "richer output" idea to nested objects.
