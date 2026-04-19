# MCPKit Examples

Runnable examples covering MCP Apps, authentication, async tasks, and proto-based code generation. Each example is self-contained with its own `go.mod`.

## Prerequisites

- **Go 1.26+**
- **Node.js + pnpm** (React app only)
- **buf** CLI (protogen only, for regenerating from `.proto`)
- An MCP host to connect to the servers: [MCPJam](https://mcpjam.com), Claude Desktop, VS Code, or Claude Code

## Examples at a Glance

| Example | What it shows | Port |
|---------|--------------|:----:|
| [apps/vanilla](apps/vanilla/) | Minimal MCP App — plain JS, no build step | 8080 |
| [apps/htmx](apps/htmx/) | Server-rendered MCP App — HTMX, zero custom JS | 8080 |
| [apps/react](apps/react/) | React 19 MCP App — hooks, Vite, TypeScript | 8080 |
| [auth/unified](auth/) | **Start here** — all auth patterns in one server | 8080 |
| [auth/bearer](auth/) | Static bearer token (simplest possible) | 8081 |
| [auth/jwt](auth/) | RS256 JWT validation via JWKS | 8082 |
| [auth/scopes](auth/) | Scope-based access control | 8083 |
| [auth/session-binding](auth/) | Session hijacking prevention | 8084 |
| [auth/public-discovery](auth/) | Pre-auth tool discovery | 8085 |
| [protogen/bookservice](protogen/bookservice/) | Proto annotations to MCP tools, resources, prompts | 8080 |
| [tasks](tasks/) | Async tool execution with lifecycle tracking (experimental) | 8080 |

## Running the Examples

### MCP Apps

Each app example starts a Go server with an MCP endpoint at `/mcp`.

```bash
# Vanilla JS — simplest possible app
cd examples/apps/vanilla
go run . -addr :8080

# HTMX — server-rendered with elicitation + sampling
cd examples/apps/htmx
go run . -addr :8080

# React — requires a frontend build first
cd examples/apps/react
pnpm install && pnpm build
cd server
go run . -addr :8080
```

Then connect your MCP host to `http://localhost:8080/mcp` (Streamable HTTP transport).

### Auth

Start with the unified example — one server, all auth patterns layered:

```bash
cd examples/auth
go run ./unified          # :8080 — JWT + scopes + session binding + public discovery
```

The server prints tokens and a walkthrough of 4 exercises. See [auth/README.md](auth/README.md) for the full guide.

Individual pattern examples are also available on separate ports:

```bash
go run ./bearer           # :8081 — static bearer token
go run ./jwt              # :8082 — JWT/JWKS
go run ./scopes           # :8083 — scope enforcement
go run ./session-binding  # :8084 — hijacking prevention
go run ./public-discovery # :8085 — pre-auth discovery
```

### Protogen (BookService)

Generates MCP tools, resources, and prompts from proto annotations:

```bash
cd examples/protogen/bookservice

# If you want to regenerate from .proto (requires buf CLI):
make generate

# Run the server:
make run          # or: go run .

# Run tests:
go test -v .
```

Connect to `http://localhost:8080/mcp` and try "Search for books about Go programming".

### Tasks (Experimental)

Async tool execution with task lifecycle (create, poll, cancel):

```bash
cd examples/tasks
go run . -addr :8080
```

Three tools: `greet` (sync-only), `slow_compute` (optional async), `failing_job` (required async). See [tasks/README.md](tasks/README.md) for the step-by-step walkthrough.

## Connecting to an MCP Host

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

1. Add server URL: `http://localhost:8080/mcp`
2. Transport: Streamable HTTP
3. For auth examples, paste the printed token into the Authorization header

## Troubleshooting

- **Port already in use** — another example is still running. Kill it or use a different `-addr` flag.
- **`go run` fails with replace directive errors** — make sure you're running from the example's own directory (where its `go.mod` lives), not from the repo root.
- **React app shows blank page** — run `pnpm build` in `examples/apps/react` before starting the Go server.
