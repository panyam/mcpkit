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

## What to Try Next

- [`sheet-music`](../sheet-music/README.md) — rung-3 sibling; the
  first place a multi-line default value trips struct-tag reflection.
- [`budget-allocator`](../budget-allocator/README.md) — rung 4, takes
  the "richer output" idea to nested objects.
