# CLAUDE.md — MCPKit

Go library for building production-grade MCP servers and clients.

## Quick Commands

```bash
make test              # Core tests (core/server/client/testutil)
make test-auth         # ext/auth sub-module
make test-ui           # ext/ui sub-module
make test-e2e          # E2E tests (auth + apps)
make testconf          # MCP conformance suite (needs Node.js)
make testconfauth      # Auth conformance (client OAuth)
make testconf-tasks    # Tasks v1 conformance (27 scenarios, self-contained)
make testconf-tasks-v2 # Tasks v2 conformance (26 scenarios, self-contained, SEP-2663)
make testconf-mrtr     # MRTR conformance (7 scenarios + 1 deferred skip, SEP-2322)
make testall           # Everything (12 stages) + Keycloak + HTML report
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
| `examples/` — Working examples (apps, auth, tasks, tasks-v2, mrtr, host, elicitation, fine-grained-auth) | `examples/README.md` |

## Sub-Modules

`ext/auth/`, `ext/ui/`, `experimental/ext/protogen/` have separate `go.mod` — `make test` does NOT cover them. Run `make tidy-all` after touching `core/` imports.

## Constraints

Project-wide: `CONSTRAINTS.md`. Per-package: `core/CONSTRAINTS.md`, `server/CONSTRAINTS.md`, `client/CONSTRAINTS.md`.

## Gotchas

- **`GH_TOKEN="$GH_PERSONAL_TOKEN"`**: EMU account can't access personal repos
- **Sub-module go.sum drift**: New `core/` imports break sub-modules until `make tidy-all`
- **Server requires initialization**: Direct `srv.Dispatch()` in tests fails — use httptest + client
- **AppHost lifecycle**: `Client.Connect()` before `AppHost.Start()`. `AppHost.Close()` only closes bridge.
- **Background goroutines**: Use `core.DetachForBackground(ctx)` (not `context.WithoutCancel`) — replaces dead POST-scoped requestFunc/notifyFunc with the session-level persistent push.
- **CORS for browser clients**: MCP servers need `Mcp-Session-Id` in both Allow-Headers and Expose-Headers, plus `DELETE` in allowed methods. Use `servicekit/middleware.CORS()` with options.
- **Demokit non-interactive + browser steps**: Steps that open a browser and expect user action will fail in `--non-interactive` mode. Interactive mode is the primary path.

Module-specific gotchas live in their READMEs.

## Conformance

Server: 30/30, Auth: 14/14, Apps: 21, Tasks v1: 27/27, Tasks v2: 26/26 (SEP-2663), MRTR: 7/7 (SEP-2322, ephemeral; 1 task-composition scenario skipped pending follow-up), Keycloak: 12/12, testall: 12/12 stages.

## Tasks v1 vs v2

Two surfaces, three entry points: `RegisterTasksV1` (frozen), `RegisterTasks` (v2/SEP-2663, canonical), `RegisterTasksHybrid` (both, dispatch by negotiated cap). See [`docs/TASKS_V2_MIGRATION.md`](docs/TASKS_V2_MIGRATION.md).
