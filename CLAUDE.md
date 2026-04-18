# CLAUDE.md — MCPKit

Go library for building production-grade MCP servers and clients.

## Quick Commands

```bash
make test              # Core tests (core/server/client/testutil)
make test-auth         # ext/auth sub-module
make test-ui           # ext/ui sub-module
make test-protogen     # experimental/ext/protogen sub-module
make test-e2e          # E2E tests (auth + apps)
make test-experimental # Experimental POC tests (telegram-events)
make testconf          # MCP conformance suite (needs Node.js)
make testconfauth      # Auth conformance (client OAuth)
make testall           # Everything (10 stages) + Keycloak + HTML report
make audit             # govulncheck + gosec + gitleaks + race
make smoke             # Curl-based transport tests
make testkcl           # Keycloak interop (needs Docker)
make upkcl / downkcl   # Keycloak container lifecycle
make tag-push V=vX.Y.Z  # Tag root + all sub-modules and push
make bump-root V=vX.Y.Z # Update mcpkit version in all sub-module go.mods
```

Sub-module commands: see `ext/ui/Makefile`, `experimental/ext/protogen/Makefile`, `experimental/telegram-events/Makefile`.

## Package Layout

| Package | What | Docs |
|---------|------|------|
| `core/` | Protocol types, typed contexts (`ToolContext`, `ResourceContext`, `PromptContext`, `MethodContext`), session APIs | `core/README.md`, `core/CONSTRAINTS.md` |
| `server/` | Server, Dispatcher, transports, middleware, registry, custom method handlers (#266) | `server/README.md`, `server/CONSTRAINTS.md` |
| `client/` | Client, transports, reconnection, auth retry | `client/README.md`, `client/CONSTRAINTS.md` |
| `ext/auth/` | JWT, PRM, OAuth (separate go.mod) | `ext/auth/docs/DESIGN.md` |
| `ext/ui/` | MCP Apps + App Bridge JS (separate go.mod) | `docs/APPS_DESIGN.md` |
| `experimental/ext/protogen/` | Proto → MCP codegen (separate go.mod) | `experimental/ext/protogen/docs/DESIGN.md` |
| `experimental/ext/tasks/` | MCP Tasks protocol (EXPERIMENTAL, separate go.mod) — middleware, store, client helpers | |
| `experimental/ext/events/` | MCP Events protocol library (EXPERIMENTAL, separate go.mod) | `experimental/ext/events/README.md` |
| `experimental/telegram-events/` | Telegram Events reference server (separate go.mod) | `experimental/telegram-events/README.md` |
| `testutil/` | `NewTestServer`, `ForAllTransports`, `TestClient` | |
| `tests/e2e/`, `tests/keycloak/` | E2E + Keycloak interop (separate go.mod) | |

## Sub-Module Checklist

- `ext/auth/`, `ext/ui/`, `experimental/ext/protogen/`, `experimental/ext/tasks/` have separate `go.mod` — `make test` does NOT cover them
- Release: `make tag-push V=vX.Y.Z` tags root + all sub-modules. Don't retag published versions.
- New core deps propagate: `make tidy-all` after touching `core/` imports
- Pre-push hook runs root + ext/auth + ext/ui + experimental/ext/protogen tests

## Gotchas

- **`GH_TOKEN="$GH_PERSONAL_TOKEN"`**: EMU account can't access personal repos
- **Import cycles**: `server/` white-box tests can't import `testutil` — use local helpers
- **Streamable HTTP returns SSE**: POST responses are `text/event-stream` with `data:` lines, not plain JSON
- **Server requires initialization**: Direct `srv.Dispatch()` in tests fails — use httptest + client instead
- **Sub-module go.sum drift**: New `core/` imports break sub-modules until `make tidy-all`
- **Telegram long-polling vs webhooks**: Mutually exclusive — delete webhook before using `GetUpdatesChan`

Module-specific gotchas live in their READMEs (protogen templates, App Bridge escaping, etc.).

## Where to Find Things

| Topic | Where |
|-------|-------|
| Architecture | `docs/ARCHITECTURE.md` |
| Capabilities list | `CAPABILITIES.md` |
| Constraints | `core/CONSTRAINTS.md`, `server/CONSTRAINTS.md`, `client/CONSTRAINTS.md` |
| Auth design | `ext/auth/docs/DESIGN.md` |
| MCP Apps design | `docs/APPS_DESIGN.md` |
| Protogen design | `experimental/ext/protogen/docs/DESIGN.md` |
| Events library | `experimental/ext/events/README.md` |
| Telegram example | `experimental/telegram-events/README.md` |
| Auth examples | `examples/auth/README.md` (5 servers: bearer, JWT, scopes, hijacking, discovery) |
| App examples | `examples/apps/` (htmx, vanilla, react — tools, elicitation, sampling, prompts) |
| Conformance baseline | `conformance/baseline.yml` |

## Conformance Status

- Server: 30/30 (40 with baseline), Auth: 14/14 (210 checks), Apps: 21, Telegram Events: 21
- Keycloak interop: 12/12 (valid token, tampered, scopes, PRM, WWW-Authenticate, password grant, session hijacking, public methods, token refresh)
- testall: 10/10 stages
- Auth examples: 5 persistent servers (bearer, JWT, scopes, session-binding, public-discovery)
