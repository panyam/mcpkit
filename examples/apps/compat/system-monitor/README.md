# system-monitor — live system stats with an app-only polling tool

Rung 6 on the [examples ladder](../README.md#reading-order--examples-ladder).
Two tools — one the model calls to render the dashboard, one the
iframe polls from inside via the bridge. First fixture introducing
`Visibility: ["app"]`.

## What it Shows

- **One model-visible tool, one app-only tool.** `get-system-info`
  appears in the model's tool dropdown; the model calls it once and
  the iframe takes over. `poll-system-stats` doesn't appear to the
  model — it's app-only (`_meta.ui.visibility = ["app"]`) and the
  iframe polls it on a timer to update the dashboard live.
- **Shared resource URI.** Both tools point at the same App iframe
  (`ui://system-monitor/mcp-app.html`) so they update the same
  rendered surface. The fixture registers the resource once (via
  `get-system-info`) and references the same URI from
  `poll-system-stats` without re-registering.

## Run Pre-Recorded

> ▶ **[Play the walkthrough in your browser](https://panyam.github.io/mcpkit/walkthroughs/examples/apps/compat/system-monitor/)** — animated playback of every curl / Go call the walkthrough makes, step-by-step. Includes both tool calls and a side-by-side look at `_meta.ui.visibility = ["app"]` on the polling tool. No clone, no setup.

## Or Run Live

### Start Server

```bash
just demo-app EXAMPLE=system-monitor
```

Starts the mcpkit-Go fixture on `http://localhost:3101/mcp` and basic-host on `http://localhost:8080`. (Pass `OPEN=1` to auto-open the browser.)

## Try It Out on basic-host

Open <http://localhost:8080> in your browser. Then:

1. Pick **System Monitor Server** from the server dropdown.
2. Pick **get-system-info** from the tool dropdown, click **Call Tool**.
3. The iframe renders the result; interact with it directly to drive subsequent tool calls (no model in the loop).

<a href="screenshots/01-dashboard.png" target="_blank"><img src="screenshots/01-dashboard.png" alt="System Monitor App: iframe shows the live dashboard with CPU / memory / disk gauges + per-process table; the dashboard updates as the iframe polls poll-system-stats every few seconds" width="50%"></a>

## Try It Out from a Host

Connect to `http://localhost:3101/mcp` from your favorite MCP host — VS Code, Claude Desktop, [MCPJam Inspector](https://github.com/MCPJam/inspector), or any spec-compliant client.

**Prompts to try** (LLM-driven hosts):

> "Show me the system monitor dashboard."
> "What does my system look like right now? CPU, memory, disk?"
> "Open the live system stats view."

The model calls `get-system-info`; the iframe renders the dashboard
and starts polling `poll-system-stats` every few seconds from inside
the iframe (not via the model).

**Verify the wire shape** (no LLM needed):

| What | How | What you should see |
|---|---|---|
| Smoke test the model-visible tool | Select `get-system-info`, call with empty input | Tool result has system info in `structuredContent`; iframe renders the dashboard |
| Verify the app-only tool's visibility | In MCPJam, find `poll-system-stats` and expand `_meta.ui` | `{"visibility": ["app"]}` — the model can't see this tool by default |
| Call the app-only tool directly | Select `poll-system-stats`, call with empty input | Tool result has live stats. In normal use the iframe drives this every few seconds. |

See [Other ways to test a fixture](../README.md#other-ways-to-test-a-fixture) in the compat README for wire inspection, upstream comparison, the strict Playwright gate, and connecting from VS Code / Claude Desktop / other MCP hosts.

## What to Try Next

- [`debug-server`](../debug-server/README.md) — rung-6 sibling, takes
  the same app-only pattern further with three tools sharing one
  iframe.
- [`integration`](../integration/README.md) — rung-6 sibling with
  host-callback semantics.
