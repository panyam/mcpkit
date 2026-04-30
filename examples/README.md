# MCPKit Examples

Runnable examples covering authentication, async tasks, MCP Apps, and proto-based code generation. Each example is self-contained with its own `go.mod`.

> **Most examples ship a guided walkthrough.** Each walkthrough is a scripted MCP host that drives the server through every wire-format detail of the feature. Two terminals: `make serve` (real MCP server, also works with VS Code/MCPJam/Claude Desktop) + `make demo` (the walkthrough). External MCP hosts are only needed when there is no `make demo`.

## Walkthrough-driven examples

The primary path. Run the demo from the CLI and read the on-disk `WALKTHROUGH.md` for the sequence diagram and step-by-step explanation.

| Example | Walkthrough covers |
|---------|--------------|
| [auth/](auth/) | Public discovery, JWT/JWKS validation, scope step-up (HTTP 403 + WWW-Authenticate per SEP-2643), session hijacking prevention |
| [tasks/](tasks/) | Async tool lifecycle (SEP-1036): sync calls, optional async, polling, progress notifications, required-task tools, cancellation |
| [tasks-v2/](tasks-v2/) | Server-directed async (SEP-2557): no client task hint, `result_type` discriminator, inlined results, tool-vs-protocol error semantics |
| [elicitation/](elicitation/) ⚠ experimental | URL-mode elicitation with consent approval (FineGrainedAuth UC1 — tracks draft [SEP-2643](https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2643)) |
| [fine-grained-auth/](fine-grained-auth/) ⚠ experimental | Authorization denial with scope step-up (UC2) + RAR per-payment ephemeral credentials (UC3) — tracks draft [SEP-2643](https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2643) |
| [host/01-apphost/](host/01-apphost/) | `AppHost` mediator — host-side app management, bidirectional tool calls |
| [host/02-multi-server/](host/02-multi-server/) | `ServerRegistry` with 3 servers, name-collision resolution |
| [events/discord/](events/discord/) ⚠ experimental | MCP Events extension — events/list, push (SSE), poll (cursor), cursorless typing source (cursor:null), webhook with TTL auto-refresh via the typed Go SDK at `experimental/ext/events/clients/go/` |
| [events/telegram/](events/telegram/) ⚠ experimental | Same protocol as discord, lighter walkthrough — focuses on the telegram-specific payload shape and the typed `Receiver[TelegramEventData]` |

```bash
cd examples/<name>
make serve   # terminal 1 — real MCP server
make demo    # terminal 2 — scripted walkthrough
```

## Examples that need an MCP host

These don't have a CLI demo — point an MCP host at the running server.

| Example | What it shows | Host needed |
|---------|--------------|-------------|
| [apps/](apps/) | MCP Apps — server-defined HTML/JS UIs in an iframe (vanilla, todolist, react, interactive, dashboard) | [MCPJam](https://mcpjam.com) (browser-based, supports the Apps extension) |
| [mrtr/](mrtr/) | SEP-2322 ephemeral MRTR — `IncompleteResult` round-trips for `elicitation/create`, `sampling/createMessage`, `roots/list`; multi-round accumulation via `requestState`; tool fixtures for `make testconf-mrtr` | Any MCP host (or run `make testconf-mrtr` for the conformance harness) |
| [protogen/bookservice/](protogen/bookservice/) | Proto annotations to MCP tools, resources, prompts | Any MCP host (Claude Code, VS Code, MCPJam, Claude Desktop) |

```bash
cd examples/apps/todolist
go run . -addr :8080
# then connect MCPJam / your host to http://localhost:8080/mcp
```

## Prerequisites

- **Go 1.26+**
- **Node.js + pnpm** — only needed for the React MCP App and the tasks TS reference server
- **buf** CLI — only needed if regenerating protogen output from `.proto`

## Connecting an external MCP host

If you're using `make serve` and want to point an external host at it (instead of `make demo`):

### Claude Code

```bash
claude mcp add my-server --transport streamable-http http://localhost:8080/mcp
```

### Claude Desktop / VS Code

Add to your MCP settings JSON:

```json
{
  "mcpServers": {
    "my-server": {
      "type": "streamable-http",
      "url": "http://localhost:8080/mcp"
    }
  }
}
```

### MCPJam

1. Server URL: `http://localhost:8080/mcp`
2. Transport: Streamable HTTP
3. For auth examples, paste the printed token into the Authorization header
4. **Required for `apps/`** — MCPJam is currently the only host with iframe support for MCP Apps

## Troubleshooting

- **Port already in use** — another example is still running. Kill it or use a different `-addr`.
- **`go run` fails with replace directive errors** — make sure you're running from the example's own directory (where its `go.mod` lives), not from the repo root.
- **MCP App shows nothing in the host** — your host needs Apps support. Try MCPJam.
