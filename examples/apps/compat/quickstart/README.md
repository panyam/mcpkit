# quickstart — minimum with the default build template

Rung 3 on the [examples ladder](../README.md#reading-order--examples-ladder).
Upstream's "quickstart" template — same `get-time` tool as the basic-*
fixtures, but the iframe ships from a default scaffolded build setup
rather than a hand-rolled minimal one.

## What it Shows

- **Same one-tool wire surface** as basic-vanillajs. The
  differentiator is the upstream-side build pipeline (a sensible
  default a developer would actually scaffold), not the protocol.
- **Bare-minimum bridge dance.** The iframe's JS uses only
  `app.ontoolresult` (render the time) and `app.callServerTool` (the
  button re-runs `get-time` via the bridge). No `app.registerTool`,
  no `app.updateModelContext`. The cleanest reference for what an
  MCP App fundamentally IS at the bridge level — contrast with
  `budget-allocator`'s 5 app-side tools.
- **`tsx` fallback in the wrapper.** Upstream's quickstart doesn't
  ship a `dist/index.js`; the test wrapper falls back to
  `npx tsx main.ts` to run the server. That fallback lives in
  `scripts/apps-playwright-docker-inner.sh`.

## Run Pre-Recorded

> ▶ **[Play the walkthrough in your browser](https://panyam.github.io/mcpkit/walkthroughs/examples/apps/compat/quickstart/)** — animated playback of every curl / Go call the walkthrough makes. Steps 1-4 walk the server-side surface (initialize → tools/list → tools/call get-time → resources/read on the iframe HTML); the closing narrative section names the bridge dance the iframe takes from there: `ontoolresult` to render the time into the DOM, and a button click that calls back to the server via `callServerTool`. The floor of an MCP App. No clone, no setup.

## Or Run Live

### Start Server

```bash
just demo-app EXAMPLE=quickstart
```

Starts the mcpkit-Go fixture on `http://localhost:3101/mcp` and basic-host on `http://localhost:8080`. (Pass `OPEN=1` to auto-open the browser.)

## Try It Out on basic-host

Open <http://localhost:8080> in your browser. Then:

1. Pick **Quickstart MCP App Server** from the server dropdown.
2. Pick **get-time** from the tool dropdown, click **Call Tool**.
3. The iframe renders the result; interact with it directly to drive subsequent tool calls (no model in the loop).

<a href="screenshots/01-get-time.png" target="_blank"><img src="screenshots/01-get-time.png" alt="quickstart App rendered in basic-host: iframe shows the ISO timestamp from get-time" width="50%"></a>

## Try It Out from a Host

Connect to `http://localhost:3101/mcp` from your favorite MCP host — VS Code, Claude Desktop, [MCPJam Inspector](https://github.com/MCPJam/inspector), or any spec-compliant client.

**Prompts to try** (LLM-driven hosts):

> "What's the current server time?"
> "Use the get-time tool."
> "What time is it right now in UTC?"

The model calls `get-time`; the iframe renders the result.

See [Other ways to test a fixture](../README.md#other-ways-to-test-a-fixture) in the compat README for wire inspection, upstream comparison, the strict Playwright gate, and connecting from VS Code / Claude Desktop / other MCP hosts.

## What to Try Next

- [`basic-vanillajs`](../basic-vanillajs/README.md) — the
  no-build-pipeline variant of the same tool.
- [`transcript`](../transcript/README.md) — different tool, still one
  per server.
- [`sheet-music`](../sheet-music/README.md) — first time a multi-line
  default trips struct-tag reflection (introduces the
  `InputSchemaPatch` escape).
