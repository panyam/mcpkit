# MCP Apps Examples

Servers exposing **MCP Apps** — interactive HTML/JS UIs that an MCP host renders inside an iframe. The host and app communicate bidirectionally via the App Bridge (postMessage protocol).

> ⚠ **MCP Apps require a host that supports the Apps extension.** [MCPJam](https://mcpjam.com) is the easiest way to test these locally — it's a browser-based MCP host with iframe support. Claude Desktop / VS Code do not yet render MCP Apps.

## Examples at a Glance

| Example | What it shows |
|---------|--------------|
| [vanilla/](vanilla/) | Minimal MCP App — plain JS, no build step |
| [todolist/](todolist/) | Server-rendered MCP App — bridge events, inline JS, elicitation + sampling |
| [react/](react/) | React 19 MCP App — hooks, Vite, TypeScript |
| [interactive/](interactive/) | Tic-tac-toe — bidirectional app-provided tools (model can call tools the app exposes) |
| [dashboard/](dashboard/) | Dashboard — tool lifecycle (enable/disable/remove at runtime) |

All apps run on port `:8080` with an MCP endpoint at `/mcp` (Streamable HTTP).

## Running

```bash
# Vanilla JS — simplest possible app, no build step
cd vanilla
go run . -addr :8080

# Todo List — server-rendered with elicitation + sampling
cd todolist
go run . -addr :8080

# React — requires a frontend build first
cd react
pnpm install && pnpm build
cd server
go run . -addr :8080

# Interactive tic-tac-toe — model + app share the tool list
cd interactive
go run . -addr :8080

# Dashboard — runtime tool registration/unregistration
cd dashboard
go run . -addr :8080
```

Then point an Apps-capable host at `http://localhost:8080/mcp`:

- **MCPJam**: open https://mcpjam.com, add server URL `http://localhost:8080/mcp`, transport: Streamable HTTP. The app UI renders in MCPJam's app pane.

## What MCP Apps offer

- **Server-defined UI**: tool authors ship the rendering, not just the data. The host gives the app a sandboxed iframe.
- **Bidirectional tools**: the app can register tools the model can call, and the model's tool calls can target either the server or the app.
- **Bridge events**: a small JS API (`MCPApp.callTool`, `MCPApp.on('event', ...)`) handles host ↔ app messaging.

See [`docs/APPS_DESIGN.md`](../../docs/APPS_DESIGN.md) for the full architecture.

## Troubleshooting

- **React app shows blank page** — run `pnpm build` in `react/` before starting the Go server.
- **Port already in use** — another example is still running. Kill it or pass a different `-addr`.
- **App pane is empty in MCPJam** — check the MCPJam network tab; the app should fetch its HTML from the `ui://` resource the server registers. If `tools/list` shows tools but no `_meta.ui` is present on a tool, that tool won't render an app.

## Related

- [App Bridge JS source](../../ext/ui/assets/) — the JS shipped to the app iframe
- [`docs/APPS_HOST.md`](../../docs/APPS_HOST.md) — implementing your own MCP App host
- [`examples/host/01-apphost`](../host/01-apphost/) — `AppHost` walkthrough (Go-side host implementation)
