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

Or run it yourself. `make demo-app` boots the mcpkit-Go fixture
(`main.go` in this folder) inside upstream's `basic-host` so you can see
the App render in a real browser. **No LLM required** — the App's bridge
JS calls `get-time` on its own and the result is inlined into the page:

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

See [Other ways to test a fixture](../README.md#other-ways-to-test-a-fixture) in the compat README for wire inspection, upstream comparison, and the strict Playwright gate.

## On the wire

You can also drive the fixture without any host UI — it's just an MCP
server. Run these three curls in order in the same shell; each one is
copy-pastable on its own:

**1. Initialize a session** — captures `Mcp-Session-Id` from the
response headers into `$SID` for the next two calls:

```bash
SID=$(curl -si -X POST http://localhost:3101/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"curl","version":"1"}}}' \
  | awk 'tolower($1) == "mcp-session-id:" {gsub(/\r/,""); print $2}')
```

**2. Acknowledge initialization** — server's dispatcher unblocks after
this `notifications/initialized`:

```bash
curl -s -X POST http://localhost:3101/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized"}'
```

**3. Call `get-time`**:

```bash
curl -s -X POST http://localhost:3101/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get-time","arguments":{}}}'
```

Expected `structuredContent`: `{"time": "2026-…Z"}`. Same shape the
iframe reads.

`_meta.ui.resourceUri` is what tells the host this tool has a UI to
render. Check it via `tools/list` — `ui://get-time/mcp-app.html` shows
up under both the nested form (`_meta.ui.resourceUri`) and the flat
form (`_meta.ui/resourceUri`) for backward compat.

## In an LLM-driven host (optional)

If you connect from a host that drives a model (Claude Desktop, VS Code
with a model, MCPJam Pro, …), you can ask things like:

> "What's the current server time?"
>
> "Get the current time and tell me what day of the week that is."
>
> "Use the get-time tool."

The model decides to call `get-time`; the iframe renders the result.
Token-cap quirks of free tiers vary — see the centralized guide for the
[other host options](../README.md#other-ways-to-test-a-fixture).

## What to look at next

- Move up the [examples ladder](../README.md#reading-order--examples-ladder) — rung 2
  shows the same shape behind frameworks; rung 3 onwards introduces
  richer payloads.
- Compare upstream's TS server side-by-side: `make demo-upstream
  EXAMPLE=basic-vanillajs` runs the TS one; the visual + wire
  surface should be identical.
- See [`main.go`](main.go) — fixture is ~60 lines.
