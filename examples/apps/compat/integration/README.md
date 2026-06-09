# integration — host-callback interactions (Send Message / Log / Link)

Rung 6 on the [examples ladder](../README.md#reading-order--examples-ladder).
Most-tested fixture in the cluster — passes the 2 standard
servers.spec.ts tests PLUS 3 host-callback interaction tests (Send
Message, Send Log, Open Link).

## What it Shows

- **Host callbacks from the App.** The App iframe surfaces buttons
  that drive **host-level** interactions over the bridge — not just
  tool calls back to the server, but messages the host itself reacts
  to (logging, link opening, notifications). Exercises the full
  three-way conversation: model ↔ server ↔ App ↔ host.
- **Multiple resources for one server.** The fixture registers a
  resource for the download URL alongside the `get-time` tool. Host
  reads the resource separately as part of the test flow.
- **5/5 in the test suite.** Two standard `loads app UI` /
  `screenshot matches golden` tests + three interaction tests
  (`Send Message button triggers host callback`, `Send Log button
  triggers host callback`, `Open Link button triggers host
  callback`). Highest pass count of any compat fixture in DOCKER mode.

## Run Pre-Recorded

> ▶ **[Play the walkthrough in your browser](https://panyam.github.io/mcpkit/walkthroughs/examples/apps/compat/integration/)** — animated playback of every curl / Go call the walkthrough makes, step-by-step. Includes the `resource:///sample-report.txt` download (the ResourceLink + ui/download-file path) alongside the get-time tool. No clone, no setup.

## Or Run Live

### Start Server

```bash
make demo-app EXAMPLE=integration
```

Starts the mcpkit-Go fixture on `http://localhost:3101/mcp` and basic-host on `http://localhost:8080`. (Pass `OPEN=1` to auto-open the browser.)

## Try It Out on basic-host

Open <http://localhost:8080> in your browser. Then:

1. Pick **Integration Test Server** from the server dropdown.
2. Pick **get-time** from the tool dropdown, click **Call Tool**.
3. The iframe renders the result; interact with it directly to drive subsequent tool calls (no model in the loop).

<a href="screenshots/01-integration-buttons.png" target="_blank"><img src="screenshots/01-integration-buttons.png" alt="Integration Test Server App: iframe with the get-time result rendered plus three buttons — Send Message, Send Log, Open Link — for host-callback interactions" width="50%"></a>

## Try It Out from a Host

Connect to `http://localhost:3101/mcp` from your favorite MCP host — VS Code, Claude Desktop, [MCPJam Inspector](https://github.com/MCPJam/inspector), or any spec-compliant client.

**Prompts to try** (LLM-driven hosts):

> "What's the current server time?"
> "Use the get-time tool."

The model calls `get-time`. The interesting bits are inside the
iframe — click "Send Message", "Send Log", or "Open Link" directly in
the App to see the host pick up the callback (basic-host renders the
message in its UI).

**Verify the wire shape** (no LLM needed):

| What | How | What you should see |
|---|---|---|
| Smoke test the tool | Select `get-time`, call with empty input | Result: `{"time": "2026-…Z"}` in `structuredContent` |
| Send Message button | In the App iframe, click "Send Message" | basic-host (host side) shows the message — App-to-host callback over the bridge |
| Send Log button | Click "Send Log" in the App | basic-host's log surface picks it up |
| Open Link button | Click "Open Link" in the App | basic-host emits an open-link event |

See [Other ways to test a fixture](../README.md#other-ways-to-test-a-fixture) in the compat README for wire inspection, upstream comparison, the strict Playwright gate, and connecting from VS Code / Claude Desktop / other MCP hosts.

## What to Try Next

- [`system-monitor`](../system-monitor/README.md) /
  [`debug-server`](../debug-server/README.md) — rung-6 siblings,
  multi-tool surfaces without host callbacks.
- [`pdf-server`](../pdf-server/README.md) — rung-7 endgame; takes the
  multi-tool + state pattern to a 9-tool surface with command queue.
