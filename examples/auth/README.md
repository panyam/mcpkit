# Auth Examples

MCP servers demonstrating mcpkit's auth capabilities. No external dependencies — no Docker, no Keycloak. Each example spins up an in-process authorization server.

> **🚀 [Skip to the guided walkthrough →](WALKTHROUGH.md)** — 8-step demokit walkthrough with sequence diagram covering public discovery, JWT/JWKS, scope step-up (403 + WWW-Authenticate), and session binding. Run it with `make serve` + `make demo`.

## Quick Start

Start with the [unified example](unified/) — one server, all four auth patterns layered together:

```bash
cd examples/auth
go run ./unified
```

The server prints tokens and a step-by-step exercise walkthrough. Connect your MCP host to `http://localhost:8080/mcp` (Streamable HTTP).

## Examples

| Port | Example | Auth Pattern |
|:----:|---------|-------------|
| 8080 | [**unified/**](unified/) | **Start here** — JWT + public discovery + scopes + session binding |
| 8081 | [bearer/](bearer/) | Static bearer token (simplest possible) |
| 8082 | [jwt/](jwt/) | RS256 JWT validation via JWKS |
| 8083 | [scopes/](scopes/) | Scope-based access control |
| 8084 | [session-binding/](session-binding/) | Session hijacking prevention |
| 8085 | [public-discovery/](public-discovery/) | Pre-auth tool discovery |

Each sub-directory has its own README with setup, exercises, and copy-pasteable prompts.

## Shared Infrastructure

The `common/` package provides the auth building blocks all examples use:

| Function | What it does |
|----------|-------------|
| `common.NewEnv(scopes)` | Spins up an in-process authorization server with JWKS + token endpoint |
| `env.NewValidator(audience)` | Creates a `JWTValidator` pointed at the AS |
| `env.MintToken(subject, scopes)` | Mints a valid RS256 JWT |
| `common.RegisterEchoTools(srv)` | Registers `echo`, `write-tool`, `admin-tool` with scope checks |

## MCP Host Configuration

See `mcp.json` for a ready-to-use multi-server configuration, or for the unified example:

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

