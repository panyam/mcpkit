# Auth Examples

Persistent MCP servers demonstrating every auth pattern mcpkit supports. Each runs on its own port with real JWT infrastructure — connect MCPJam, VS Code, or any MCP host.

## Quick Start

```bash
cd examples/auth

# Run all 5 on different ports:
go run ./bearer           # :8081 — static bearer token
go run ./jwt              # :8082 — JWT/JWKS validation
go run ./scopes           # :8083 — scope enforcement
go run ./session-binding  # :8084 — hijacking prevention
go run ./public-discovery # :8085 — pre-auth discovery
```

Each server prints the token(s) to use. Copy-paste into MCPJam's Authorization header.

## Examples

| Port | Example | Auth Pattern | What to Try |
|:---:|---------|-------------|------------|
| 8081 | **bearer/** | Static token | Connect with `Bearer my-secret-token` |
| 8082 | **jwt/** | RS256 JWT via JWKS | Server prints a valid token on startup |
| 8083 | **scopes/** | Scope enforcement | Three tokens with different scopes — try each |
| 8084 | **session-binding/** | Hijacking prevention | Connect as alice, then try bob's token on alice's session |
| 8085 | **public-discovery/** | Pre-auth tools/list | Connect WITHOUT token — discover tools, then authenticate to call them |

## VS Code / MCP Host Configuration

See `mcp.json` for a ready-to-use configuration. Run all examples first, then point your MCP host at the URLs.

## How It Works

Each example (except bearer) spins up an in-process authorization server (oneauth `TestAuthServer`) that provides real JWKS, token endpoint, and RS256 signing. No external dependencies — no Docker, no Keycloak, no hosting.

The `common/` package provides shared setup:
- `common.NewEnv(scopes)` — creates the in-process AS
- `env.NewValidator(audience)` — creates JWTValidator
- `env.MintToken(subject, scopes)` — creates valid JWTs
- `common.RegisterEchoTools(srv)` — registers echo, write-tool, admin-tool

## Related

- `tests/e2e/` — E2E auth integration tests
- `tests/keycloak/` — Keycloak interop tests (real OIDC)
- `ext/auth/docs/DESIGN.md` — Auth architecture
