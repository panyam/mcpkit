# URL Elicitation — Consent Approval Flow (UC1)

> ⚠ **EXPERIMENTAL** — Tracks [SEP-2643](https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2643) (Structured Authorization Denials), currently a draft. Wire format may change as the SEP evolves.

A scripted MCP host walking through the **UC1 consent approval flow**: a tool call gets denied with a JSON-RPC `-32042` (URLElicitationRequired) carrying a consent URL; the host opens the URL in a browser; the user approves; the server pushes a `notifications/elicitation/complete` notification over SSE; the host auto-retries with the `authorizationContextId` and gets the result.

## Quick Start

```bash
# Terminal 1 — start the MCP server
make serve

# Terminal 2 — run the scripted walkthrough
make demo
```

The walkthrough opens a browser to `http://localhost:8080/approve?ctx=...`. Click **Approve** there; the demo auto-retries and completes.

See [WALKTHROUGH.md](WALKTHROUGH.md) for the full sequence diagram and step-by-step description (regenerate via `make readme`).

## What it demonstrates

- The **`-32042` URLElicitationRequired** denial shape — `error.code` + `error.data.authorization.authorizationContextId` + `error.data.elicitations[].url`.
- The **GET SSE notification stream** carrying `notifications/elicitation/complete` from server to host while the user interacts with the consent URL out-of-band.
- The **auto-retry pattern**: host parses the denial, opens the URL, waits for the SSE notification, then re-issues `tools/call` with `_meta.authorizationContextId` set to the captured value.
- The **server-side consent middleware** that intercepts `tools/call` for protected tools, mints a context, denies with the URL, persists approval state, and lets the retry through once approved.
- **CORS configuration** for browser-based MCP hosts (MCPJam) — `Mcp-Session-Id` in both Allow-Headers and Expose-Headers, `DELETE` in allowed methods.

## Where to look in the code

- Walkthrough steps + denial parsing: [`main.go`](main.go) (the `runDemo` block)
- Consent middleware + store + `/approve` handler: [`main.go`](main.go) (after `serve()`)
- SEP-2643 wire constants: [`core.MetaKeyAuthorizationContextID`](../../core/meta.go) and the `-32042` URLElicitationRequired error code
- Companion: [`examples/fine-grained-auth/`](../fine-grained-auth/) — UC2 (scope step-up) + UC3 (RAR per-payment credentials)

## Notes

- CORS is applied via `server.WithHandlerWrap(cors)` so it covers `/mcp` plus the `/approve` route registered through `server.WithMux`. Browser-based MCP hosts (MCPJam) need `Mcp-Session-Id` in both Allow- and Expose-Headers; the canonical CORS configuration lives in this example's `serve()` block.
- The walkthrough's "Approve" button click is a real human-in-the-loop step; CI runs would need either a headless browser or a synthetic `POST /approve?ctx=...`.
