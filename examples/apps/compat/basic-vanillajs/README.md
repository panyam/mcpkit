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

Start here — boots the mcpkit-Go fixture (`main.go` in this folder) and
opens [MCPJam Inspector](https://github.com/MCPJam/inspector) so you can
poke at the protocol surface:

```bash
make demo-app EXAMPLE=basic-server-vanillajs
```

What runs:

- **mcpkit-Go fixture** on `http://localhost:3101/mcp` — the Go binary
  built from `main.go` in this folder.
- **MCPJam Inspector** opens in your default browser. Paste
  `http://localhost:3101/mcp` into MCPJam's server list and connect.
  Then browse `tools/list`, `_meta.ui`, and tool-call payloads on the
  wire.

### [Optional] You can also do…

- **See the App rendered in basic-host (Go fixture).** Same Go server,
  but driven by basic-host (the canonical reference UI). Opens a
  browser at `http://localhost:8080`:

  ```bash
  RENDERER=basic-host make demo-app EXAMPLE=basic-server-vanillajs
  ```

- **Hit upstream's TS reference server instead.** Useful for comparing
  the Go fixture's wire surface against the canonical implementation:

  ```bash
  make demo-upstream EXAMPLE=basic-server-vanillajs
  ```

  Add `RENDERER=basic-host` to render the upstream TS in basic-host
  instead of MCPJam.

- **Strict parity check against the mcpkit-Go fixture.** Runs upstream's
  Playwright suite against the Go binary — wire-level `tools/list` diff
  + visual PNG gate. Requires Docker:

  ```bash
  EXAMPLE=basic-server-vanillajs make test-apps-playwright-docker
  ```

## Prompts to try

In [MCPJam Inspector](https://github.com/MCPJam/inspector) or
basic-host, connect to `Basic MCP App Server (Vanilla JS)`, then paste
any of these into the chat:

```
What's the current server time?
```

![basic-vanillajs App rendered in basic-host: iframe with a button showing the ISO timestamp returned by get-time](screenshots/01-get-time.png)

```
Get the current time and tell me what day of the week that is.
```

```
Use the get-time tool.
```

Any of these should make the model call `get-time` and inline the
result. The App iframe also renders a button — click it directly and
the App calls `get-time` itself via the bridge (no model in the loop).

### Direct tool call (no LLM needed)

If you're running MCPJam without an LLM connected, or want to verify
the wire shape:

| What | How | What you should see |
|---|---|---|
| Smoke test the tool | Select `get-time` from the dropdown and click "Call Tool" with empty input | Tool result panel: `{ "time": "2026-…Z" }` in `structuredContent` |
| Check `_meta.ui.resourceUri` | Expand the tool's `_meta` field in the tools list | `{"ui": {"resourceUri": "ui://get-time/mcp-app.html"}, "ui/resourceUri": "ui://get-time/mcp-app.html"}` — both the nested and flat forms are emitted for backward compat. |

## What to look at next

- Move up the [examples ladder](../README.md#reading-order--examples-ladder) — rung 2
  shows the same shape behind frameworks; rung 3 onwards introduces
  richer payloads.
- Compare upstream's TS server side-by-side: `make demo-upstream
  EXAMPLE=basic-server-vanillajs` runs the TS one; the visual + wire
  surface should be identical.
- See [`main.go`](main.go) — fixture is ~60 lines.
