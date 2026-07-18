# basic-vanillajs — minimum-viable MCP App in Go

The simplest fixture in [`examples/apps/compat`](../) — one tool, one
resource, vanilla-JS iframe. Start here if you're new to the
host ↔ server ↔ App-iframe round trip.

## What it Shows

- **One tool** — `get-time` returns the current server time as an
  ISO 8601 string. Typed handler (`struct{}` input, `getTimeOutput`
  output); the JSON Schema is reflected from the struct.
- **One resource** — `ui://get-time/mcp-app.html` serves upstream's
  verbatim iframe HTML. The host reads it at startup and inlines into
  the iframe on first tool call.
- **No frontend framework** — the App HTML is hand-rolled with
  `document.createElement` + `addEventListener`. Demonstrates that the
  protocol surface is framework-agnostic; later examples
  (`basic-preact`, `basic-react`, ...) plug Preact / React / Solid /
  Svelte / Vue behind the same wire.

## Run Pre-Recorded

> ▶ **[Play the walkthrough in your browser](https://panyam.github.io/mcpkit/walkthroughs/examples/apps/compat/basic-vanillajs/)** — animated playback of every curl / Go call the walkthrough makes, step-by-step. No clone, no setup.

## Or Run Live

### Start Server

```bash
just demo-app EXAMPLE=basic-vanillajs
```

Starts the mcpkit-Go fixture on `http://localhost:3101/mcp` and basic-host on `http://localhost:8080`. (Pass `OPEN=1` to auto-open the browser.)

## Try It Out on basic-host

Open <http://localhost:8080> in your browser. Then:

1. Pick **Basic MCP App Server (Vanilla JS)** from the server dropdown.
2. Pick **get-time** from the tool dropdown, click **Call Tool** with empty input.
3. The iframe inlines the ISO 8601 timestamp. The App also renders a button — click it and the App calls `get-time` itself via the bridge (no model in the loop).

<a href="screenshots/01-get-time.png" target="_blank"><img src="screenshots/01-get-time.png" alt="basic-vanillajs App rendered in basic-host: iframe with a button showing the ISO timestamp returned by get-time" width="50%"></a>

## Try It Out from a Host

Connect to `http://localhost:3101/mcp` from your favorite MCP host — VS Code, Claude Desktop, [MCPJam Inspector](https://github.com/MCPJam/inspector), or any spec-compliant client.

**Prompts to try** (LLM-driven hosts):

> "What's the current server time?"
> "Use the get-time tool."

The model calls `get-time`; the iframe renders the result.

See [Other ways to test a fixture](../README.md#other-ways-to-test-a-fixture) in the compat README for wire inspection, upstream comparison, the strict Playwright gate, and connecting from VS Code / Claude Desktop / other MCP hosts.

## What to Try Next

- Move up the [examples ladder](../README.md#reading-order--examples-ladder) — rung 2 (`basic-preact`, `basic-react`, `basic-solid`, `basic-svelte`, `basic-vue`) shows the same shape behind frameworks; rung 3 onwards introduces richer payloads.
- Compare upstream's TS server side-by-side: `just demo-upstream EXAMPLE=basic-vanillajs` runs the TS one; the visual + wire surface should be identical.
- See [`main.go`](main.go) — fixture is ~60 lines.
