# basic-vanillajs — minimum-viable MCP App in Go

The simplest fixture in [`examples/apps/compat`](../) — one tool, one
resource, vanilla-JS iframe. Start here if you're new to the
host ↔ server ↔ App-iframe round trip.

## What it shows

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

## Run it

> ▶ **[Play the walkthrough in your browser](https://panyam.github.io/mcpkit/walkthroughs/examples/apps/compat/basic-vanillajs/)** — animated playback of every curl / Go call the walkthrough makes, step-by-step. No clone, no setup.

Boots the mcpkit-Go fixture (`main.go` in this folder) inside upstream's
`basic-host` so you can see the App render in a real browser. **No LLM
required** — the App's bridge JS calls `get-time` on its own and the
result is inlined into the page:

```bash
make demo-app EXAMPLE=basic-vanillajs
```

A browser opens at `http://localhost:8080`. First-touch flow:

1. Pick **Basic MCP App Server (Vanilla JS)** from the server dropdown.
2. Pick **get-time** from the tool dropdown, click **Call Tool** with
   empty input.
3. The iframe inlines the ISO 8601 timestamp. The App also renders a
   button — click it and the App calls `get-time` itself via the bridge
   (no model in the loop).

<a href="screenshots/01-get-time.png" target="_blank"><img src="screenshots/01-get-time.png" alt="basic-vanillajs App rendered in basic-host: iframe with a button showing the ISO timestamp returned by get-time" width="50%"></a>

See [Other ways to test a fixture](../README.md#other-ways-to-test-a-fixture) in the compat README for wire inspection, upstream comparison, the strict Playwright gate, and connecting from VS Code / Claude Desktop / other MCP hosts.

### Verify the wire shape (no LLM needed)

Useful for spot-checking what the Go fixture puts on the wire vs. what the iframe reads:

| What | How | What you should see |
|---|---|---|
| Smoke test the tool | Call `get-time` with empty input (basic-host or MCPJam) | Tool result `structuredContent`: `{"time": "2026-…Z"}` — same shape the iframe inlines |
| Check `_meta.ui.resourceUri` | Expand the tool's `_meta` field in `tools/list` | `{"ui": {"resourceUri": "ui://get-time/mcp-app.html"}, "ui/resourceUri": "ui://get-time/mcp-app.html"}` — both the nested and flat forms are emitted for backward compat |

## What to look at next

- Move up the [examples ladder](../README.md#reading-order--examples-ladder) — rung 2
  shows the same shape behind frameworks; rung 3 onwards introduces
  richer payloads.
- Compare upstream's TS server side-by-side: `make demo-upstream
  EXAMPLE=basic-vanillajs` runs the TS one; the visual + wire
  surface should be identical.
- See [`main.go`](main.go) — fixture is ~60 lines.
