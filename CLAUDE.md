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
make testconf-tasks    # Tasks v1 conformance (27 scenarios, local)
make testconf-tasks-v2 # SEP-2663 — fork @ MCPCONFORMANCE_TASKS_V2_PATH + mcpkit-local stricter sentinel
make testconf-mrtr     # SEP-2322 — fork @ MCPCONFORMANCE_MRTR_PATH + mcpkit-local stricter sentinel
make testconf-list-ttl # SEP-2549 — fork @ MCPCONFORMANCE_LIST_TTL_PATH (5 checks, 3 fixtures)
make testconf-file-inputs # SEP-2356 — fork @ MCPCONFORMANCE_FILE_INPUTS_PATH (7 checks)
make testconf-auth-server # MCP authz 2025-11-25 — fork @ MCPCONFORMANCE_AUTH_PATH (6 scenarios, 23 checks: 18 active, 5 SKIPPED gaps for unsupported features)
make testall           # Everything (9 stages, 18 sub-stages) + Keycloak + HTML report
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
- **SEP-2549 `ttlMs` uses `*int` + omitempty**: the merged spec treats an absent `ttlMs` the same as `0` (both "immediately stale"), so the field is two-state client-side. mcpkit still types it `*int` so a server can emit an explicit `ttlMs: 0` distinct from omitting the field — plain `int` + omitempty cannot. `cacheScope` is a plain `string` (omitempty); absent defaults to `"public"` client-side. Field was renamed from `ttl` (seconds) during the spec's final review. See `docs/LIST_TTL_MIGRATION.md`.
- **Conformance suites are brand-neutral**: `conformance/*/` assert what the spec says, not what mcpkit does. mcpkit-specific behavior (e.g., echoing SEP-2243 routing headers on responses) belongs in `server/*_test.go`, not in conformance scenarios. The conformance suites are the marketing — keep them framed as "what any server must do."
- **`MCPCONFORMANCE_*_PATH` (per-suite, defined in `conformance/Makefile`)**: each `testconf-*` target points at its own worktree of the [`panyam/mcpconformance`](https://github.com/panyam/mcpconformance) fork because different SEPs live on different branches while their upstream PRs are still draft. Defaults (resolved relative to `conformance/Makefile`): `MCPCONFORMANCE_TASKS_V2_PATH` and `MCPCONFORMANCE_MRTR_PATH` → `../conf-template` (`feat/tasks-mrtr-extension`); `MCPCONFORMANCE_FILE_INPUTS_PATH`, `MCPCONFORMANCE_LIST_TTL_PATH`, and `MCPCONFORMANCE_AUTH_PATH` → `../conf-pending` (`pending`). Override per-invocation when a SEP splits to its own branch waiting upstream approval. Each target fail-fasts with a remediation message if its path is missing.

Module-specific gotchas live in their READMEs.

## Conformance

Server 30/30, Auth 14/14, Apps 21, Tasks v1 27/27, Tasks v2 8 classes / ~33 checks (SEP-2663, fork), MRTR 1 class / 7 + 1 skip (SEP-2322, fork), List-TTL 5/5 (SEP-2549), File-Inputs 7/7 (SEP-2356), Keycloak 12/12, testall 9/9 logical stages (18 sub-stages including all 7 conformance suites). Tasks v2 + MRTR live in the upstream-portable [`panyam/mcpconformance`](https://github.com/panyam/mcpconformance) fork; see CAPABILITIES.md `mcp-tasks-v2-conformance`.

## Tasks v1 vs v2

Two surfaces, three entry points: `RegisterTasksV1` (frozen), `RegisterTasks` (v2/SEP-2663, canonical), `RegisterTasksHybrid` (both, dispatch by negotiated cap). See [`docs/TASKS_V2_MIGRATION.md`](docs/TASKS_V2_MIGRATION.md).
