# CLAUDE.md — MCPKit

Go library for building production-grade MCP servers and clients.

## Quick Commands

```bash
make test              # Core tests (core/server/client/testutil)
make test-auth         # ext/auth sub-module
make test-ui           # ext/ui sub-module
make test-e2e          # E2E tests (auth + apps)
make testconf          # MCP conformance suite (needs Node.js)
make testall           # Everything (10 stages) + Keycloak + HTML report
make audit             # govulncheck + gosec + gitleaks + race
make tag-push V=vX.Y.Z # Tag root + all sub-modules and push
```

## Package Layout

| Package | Docs |
|---------|------|
| `core/` — Protocol types, typed contexts, session APIs | `core/README.md`, `core/CONSTRAINTS.md` |
| `server/` — Server, transports, middleware, tasks | `server/README.md`, `server/CONSTRAINTS.md` |
| `client/` — Client, transports, reconnection, auth retry | `client/README.md`, `client/CONSTRAINTS.md` |
| `ext/auth/` — JWT, PRM, OAuth (separate go.mod) | `ext/auth/docs/DESIGN.md` |
| `ext/ui/` — MCP Apps, Bridge JS, AppHost, ServerRegistry (separate go.mod) | `docs/APPS_DESIGN.md`, `docs/APPS_HOST.md`, `docs/APPS_ONBOARDING.md` |
| `experimental/ext/protogen/` — Proto → MCP codegen | `experimental/ext/protogen/docs/DESIGN.md` |
| `experimental/ext/events/` — MCP Events protocol | `experimental/ext/events/README.md` |
| `testutil/` — Test helpers | |
| `tests/e2e/`, `tests/keycloak/` — Integration tests | `tests/e2e/apps/README.md` |
| `examples/` — Working examples (apps, auth, tasks, host) | `examples/README.md` |

## Sub-Modules

`ext/auth/`, `ext/ui/`, `experimental/ext/protogen/` have separate `go.mod` — `make test` does NOT cover them. Run `make tidy-all` after touching `core/` imports.

## Constraints

Project-wide: `CONSTRAINTS.md`. Per-package: `core/CONSTRAINTS.md`, `server/CONSTRAINTS.md`, `client/CONSTRAINTS.md`.

## Gotchas

- **`GH_TOKEN="$GH_PERSONAL_TOKEN"`**: EMU account can't access personal repos
- **Sub-module go.sum drift**: New `core/` imports break sub-modules until `make tidy-all`
- **Server requires initialization**: Direct `srv.Dispatch()` in tests fails — use httptest + client
- **AppHost lifecycle**: `Client.Connect()` before `AppHost.Start()`. `AppHost.Close()` only closes bridge.

Module-specific gotchas live in their READMEs.

## Conformance

Server: 30/30, Auth: 14/14, Apps: 21, Tasks: 27/27, Keycloak: 12/12, testall: 10/10 stages.
