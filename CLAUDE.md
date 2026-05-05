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
make testconf-tasks-v2 # Tasks v2 conformance (33 scenarios, self-contained, SEP-2663)
make testconf-mrtr     # MRTR conformance (7 scenarios + 1 deferred skip, SEP-2322)
make testconf-list-ttl # List-TTL conformance (5 scenarios, SEP-2549)
make testconf-file-inputs # File-inputs conformance (7 scenarios, SEP-2356; 4 green / 3 red awaiting #361 + #362)
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
| `examples/` — Working examples (apps, auth, tasks, tasks-v2, mrtr, list-ttl, host, elicitation, fine-grained-auth) | `examples/README.md` |

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
- **Three-state TTL needs `*int` + omitempty**: SEP-2549 distinguishes `nil` (no guidance), `&0` (do not cache), `&N>0` (fresh for N seconds). Plain `int` with omitempty would conflate `nil` with `&0`. Same pattern fits any spec field with explicit-zero semantics.
- **Conformance suites are brand-neutral**: `conformance/*/` assert what the spec says, not what mcpkit does. mcpkit-specific behavior (e.g., echoing SEP-2243 routing headers on responses) belongs in `server/*_test.go`, not in conformance scenarios. The conformance suites are the marketing — keep them framed as "what any server must do."

Module-specific gotchas live in their READMEs.

## Conformance

Server: 30/30, Auth: 14/14, Apps: 21, Tasks v1: 27/27, Tasks v2: 33/33 (SEP-2663), MRTR: 7/7 (SEP-2322, ephemeral; 1 task-composition scenario skipped pending follow-up), List-TTL: 5/5 (SEP-2549), File-Inputs: 4/7 (SEP-2356; 3 awaiting #361 server validation + #362 capability gating), Keycloak: 12/12, testall: 12/12 stages.

## Tasks v1 vs v2

Two surfaces, three entry points: `RegisterTasksV1` (frozen), `RegisterTasks` (v2/SEP-2663, canonical), `RegisterTasksHybrid` (both, dispatch by negotiated cap). See [`docs/TASKS_V2_MIGRATION.md`](docs/TASKS_V2_MIGRATION.md).
