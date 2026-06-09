# debug-server — kitchen-sink diagnostic + two app-only helpers

Rung 6 on the [examples ladder](../README.md#reading-order--examples-ladder).
Three tools — a kitchen-sink debug surface for the model, plus two
app-only helpers (refresh / log) the iframe uses internally.

## What it Shows

- **Three tools, shared iframe.** `debug-tool` is the model-visible
  kitchen-sink for testing content-type behavior (text, image, audio,
  resource, mixed), error simulation, delays, large input, and
  structured content. `debug-refresh` and `debug-log` are app-only
  helpers the iframe calls via the bridge.
- **`Payload any` reflects cleanly.** Before the issue 548 Gap 2 fix,
  `debug-log`'s `Payload any` field required an `InputSchemaOverride`
  because invopop emits bare-`true` for `any` (rejected by the MCP
  TypeScript SDK's zod validator). With the fix in place, the field
  reflects to `{}` automatically — no override.

## Run Pre-Recorded

> ▶ **[Play the walkthrough in your browser](https://panyam.github.io/mcpkit/walkthroughs/examples/apps/compat/debug-server/)** — animated playback of every curl / Go call the walkthrough makes, step-by-step. Exercises all three tools (debug-tool kitchen sink, debug-refresh polling, debug-log event log) including the App-only visibility flag on the latter two. No clone, no setup.

## Or Run Live

### Start Server

```bash
make demo-app EXAMPLE=debug-server
```

Starts the mcpkit-Go fixture on `http://localhost:3101/mcp` and basic-host on `http://localhost:8080`. (Pass `OPEN=1` to auto-open the browser.)

## Try It Out on basic-host

Open <http://localhost:8080> in your browser. Then:

1. Pick **Debug MCP App Server** from the server dropdown.
2. Pick **debug-tool** from the tool dropdown, click **Call Tool**.
3. The iframe renders the result; interact with it directly to drive subsequent tool calls (no model in the loop).

<a href="screenshots/01-debug-text.png" target="_blank"><img src="screenshots/01-debug-text.png" alt="Debug Server App: iframe shows the kitchen-sink debug surface with content-type / multiple-blocks / structured-content / error toggles; a recent text-content response is displayed" width="50%"></a>

## Try It Out from a Host

Connect to `http://localhost:3101/mcp` from your favorite MCP host — VS Code, Claude Desktop, [MCPJam Inspector](https://github.com/MCPJam/inspector), or any spec-compliant client.

**Prompts to try** (LLM-driven hosts):

> "Use the debug tool to show me text content."
> "Debug tool with content type "image" and include structured content."
> "Run the debug tool with mixed content blocks and a 500ms delay."
> "Simulate an error in the debug tool."
> "Send a debug call with a large input string (say 10KB of text)."

The model calls `debug-tool` with the corresponding parameters; the
iframe renders the result and exercises whichever content shape was
requested.

**Verify the wire shape** (no LLM needed):

| What | How | What you should see |
|---|---|---|
| Text-only response | Select `debug-tool`, call with `{"contentType": "text"}` | Tool result is a text block, `structuredContent` has counter + timestamp |
| Mixed-content response | Call with `{"contentType": "mixed", "multipleBlocks": true}` | Multiple content items: text + image + audio etc. |
| Simulate error | Call with `{"simulateError": true}` | Tool result has `isError: true` + an error message |
| App-only refresh | Select `debug-refresh`, call with empty input | Tool result has fresh timestamp + counter. (App-only — model won't see this in its tool list by default.) |
| App-only log | Select `debug-log`, call with `{"type": "test", "payload": {"any": "shape"}}` | Tool result confirms logged. Note the `payload` field accepts any shape — that's the issue-548 Gap 2 fix working. |

See [Other ways to test a fixture](../README.md#other-ways-to-test-a-fixture) in the compat README for wire inspection, upstream comparison, the strict Playwright gate, and connecting from VS Code / Claude Desktop / other MCP hosts.

## What to Try Next

- [`system-monitor`](../system-monitor/README.md) — rung-6 sibling
  with two tools (one app-only) sharing the iframe.
- [`integration`](../integration/README.md) — rung-6 sibling that
  adds host-callback semantics.
- [`pdf-server`](../pdf-server/README.md) — rung-7 endgame for
  multi-tool fixtures.
