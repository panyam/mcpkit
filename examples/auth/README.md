# Auth Examples

Standalone examples demonstrating every auth pattern mcpkit supports. Each runs fully in-process — no Docker, no browser, no hosting. Just `go run` and see the output.

## Quick Start

```bash
cd examples/auth
go run ./bearer           # simplest: static token
go run ./jwt              # JWT/JWKS validation + claims
go run ./scopes           # scope enforcement + step-up
go run ./session-binding  # hijacking prevention
go run ./public-discovery # pre-auth capability discovery
```

## Examples

| Example | What it demonstrates | Auth mechanism |
|---------|---------------------|---------------|
| **bearer/** | Static bearer token validation | `server.WithBearerToken("secret")` |
| **jwt/** | RS256 JWT via JWKS, claims propagation | `auth.NewJWTValidator()` + oneauth AS |
| **scopes/** | Scope-based access control + step-up | `auth.RequireScope()` |
| **session-binding/** | Session hijacking prevention | Principal binding via `Claims.Subject` |
| **public-discovery/** | Discover tools before authenticating | `server.WithPublicMethods()` |

## How They Work

Each example spins up an in-process authorization server (oneauth `TestAuthServer`) alongside an mcpkit MCP server. Tokens are real RS256 JWTs validated via a real JWKS endpoint — the full auth stack runs in-process.

The examples are **run-and-exit demos**, not persistent servers. They print step-by-step output showing what happens at each stage (connect, reject, step-up, etc.), then exit.

## Screenshots

<!-- TODO: add terminal output screenshots -->

## Related

- `tests/e2e/` — E2E auth integration tests (same auth stack, test assertions)
- `tests/keycloak/` — Keycloak interop tests (real OIDC provider)
- `ext/auth/docs/DESIGN.md` — Auth architecture and spec compliance
