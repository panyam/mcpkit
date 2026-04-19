# Auth Examples

MCP servers demonstrating mcpkit's auth capabilities. Start with the **unified** example — one server, all four auth patterns layered together. The individual examples are useful as copy-paste starting points for specific patterns.

## Quick Start — Unified Example

```bash
cd examples/auth
go run ./unified
```

The server starts on `:8080` and prints tokens for each exercise. Connect your MCP host (MCPJam, Claude Desktop, VS Code) to `http://localhost:8080/mcp` using Streamable HTTP transport.

## Exercises

Work through these in order. Each builds on the previous one.

### 1. Public Discovery (no token needed)

Connect to the server **without** an Authorization header.

- **tools/list** works — you can see what tools are available
- **Call echo** — returns 401, tool execution requires authentication

This is the `WithPublicMethods` pattern: let clients discover capabilities before authenticating.

### 2. JWT Authentication

Copy the **read-only token** from the server output. Connect with `Authorization: Bearer <token>`.

- **Call echo** with any message — it now works and reports your identity: `echo: hello (user: alice, scopes: [read])`

The server validates RS256 JWTs against an in-process JWKS endpoint. No external auth server needed — `common.NewEnv()` spins up everything.

### 3. Scope Enforcement

Still connected with the read-only token:

- **Call write-tool** — fails: `error: insufficient scope: requires "write"`
- **Call admin-tool** — fails: `error: insufficient scope: requires "admin"`

Now disconnect and reconnect with the **read+write token**:

- **Call write-tool** — works
- **Call admin-tool** — still fails (missing `admin` scope)

Reconnect with the **all-scopes token** — everything works.

### 4. Session Binding

Connect as alice (any of her tokens). Note the session ID.

Now try sending a request with **bob's token** on alice's session (same `Mcp-Session-Id` header, different `Authorization` header) — the server returns **403 Forbidden**.

The server binds `Claims.Subject` to the session at creation time. A different principal can't hijack an existing session.

## How It Works

The unified server composes four `server.Option` calls:

```go
srv := server.NewServer(
    core.ServerInfo{Name: "auth-unified", Version: "1.0"},
    server.WithAuth(validator),                  // JWT validation + session binding
    server.WithPublicMethods("initialize", ...),  // pre-auth discovery
    server.WithMiddleware(server.LoggingMiddleware(log.Default())),
)
```

- **JWT + session binding** are both handled by `WithAuth` — the transport automatically binds the token's `sub` claim to the session
- **Public discovery** is additive via `WithPublicMethods`
- **Scope enforcement** happens at the tool level with `auth.RequireScope(ctx, "write")`

The `common/` package provides the shared infrastructure all examples use:

| Function | What it does |
|----------|-------------|
| `common.NewEnv(scopes)` | Spins up an in-process authorization server with JWKS + token endpoint |
| `env.NewValidator(audience)` | Creates a `JWTValidator` pointed at the AS |
| `env.MintToken(subject, scopes)` | Mints a valid RS256 JWT |
| `common.RegisterEchoTools(srv)` | Registers `echo`, `write-tool`, `admin-tool` with scope checks |

## Individual Examples

Each runs on its own port and isolates a single pattern — useful as copy-paste templates.

```bash
go run ./bearer           # :8081 — static bearer token (simplest possible auth)
go run ./jwt              # :8082 — JWT/JWKS validation
go run ./scopes           # :8083 — scope enforcement
go run ./session-binding  # :8084 — hijacking prevention
go run ./public-discovery # :8085 — pre-auth discovery
```

## VS Code / MCP Host Configuration

See `mcp.json` for a ready-to-use multi-server configuration. For the unified example:

```json
{
  "mcpServers": {
    "auth-unified": {
      "type": "streamable-http",
      "url": "http://localhost:8080/mcp"
    }
  }
}
```

## Related

- `ext/auth/docs/DESIGN.md` — Auth architecture
- `tests/e2e/` — E2E auth integration tests
- `tests/keycloak/` — Keycloak interop tests (real OIDC)
