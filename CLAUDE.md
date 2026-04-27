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
make testconf-tasks    # Tasks v1 conformance (27 scenarios, self-contained)
make testconf-tasks-v2 # Tasks v2 conformance (19 scenarios, self-contained, skips MRTR)
make testall           # Everything (12 stages) + Keycloak + HTML report
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
| `server/task_*.go`, `server/tasks_experimental.go` | MCP Tasks protocol (EXPERIMENTAL) — middleware, store, handlers, TaskContext, TaskCallbacks |
| `experimental/ext/events/` | MCP Events protocol library (EXPERIMENTAL, separate go.mod) | `experimental/ext/events/README.md` |
| `experimental/telegram-events/` | Telegram Events reference server (separate go.mod) | `experimental/telegram-events/README.md` |
| `testutil/` | `NewTestServer`, `ForAllTransports`, `TestClient` | |
| `tests/e2e/`, `tests/keycloak/` | E2E + Keycloak interop (separate go.mod) | |

## Sub-Module Checklist

- `ext/auth/`, `ext/ui/`, `experimental/ext/protogen/` have separate `go.mod` — `make test` does NOT cover them
- Tasks moved to `server/` (no separate go.mod) — covered by `make test`
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
- **MCP App CSP**: Host iframes enforce strict CSP (`script-src 'unsafe-inline'`, no `connect-src`). No external CDN scripts, no `fetch()` to server. Use inline JS + bridge events only.
- **Background goroutine requestFunc**: Task goroutines inherit a dead POST-scoped `requestFunc`. Use `core.DetachForBackground(ctx)` instead of `context.WithoutCancel(ctx)` — it replaces both `requestFunc` and `notifyFunc` with the session-level persistent push.
- **Tasks side-channel**: `TaskElicit`/`TaskSample` send via the `tasks/result` handler's live connection, not the background goroutine's dead one. The handler proxies requests from a channel.
- **POST SSE writer closure**: After `handlePostSSE` returns, the SSE writer is marked `closed`. Background goroutines that try to notify via the dead writer get silent no-ops instead of panics.
- **Task cancel race**: After cancel, the background goroutine checks if the task is already terminal before setting status. `StoreTerminalResult` also guards against terminal→terminal transitions.
- **Initialize returns SSE or JSON**: Go server returns initialize as SSE when the client sends `Accept: text/event-stream` (matching TS SDK), or plain JSON otherwise. Curl helpers should send the appropriate Accept header.
- **Conformance assertions — spec MUST vs MAY**: Don't assert specific error codes unless the spec mandates them. TTL is a client hint (server may ignore). `pollInterval` is server-only (not a client request param — TS SDK bug). Notifications are optional. Auth-context binding ≠ session isolation. Use `ENFORCE_ERROR_CODES` flag pattern for future-proofing.
- **Flaky TestStoreConcurrentAccess**: Was caused by timestamp-based task IDs colliding when goroutines ran within the same nanosecond. Fixed with deterministic IDs (`fmt.Sprintf`).

Module-specific gotchas live in their READMEs (protogen templates, App Bridge escaping, etc.).

## Where to Find Things

| Topic | Where |
|-------|-------|
| Architecture | `docs/ARCHITECTURE.md` |
| Capabilities list | `CAPABILITIES.md` |
| Constraints | `CONSTRAINTS.md` (project-wide), `core/CONSTRAINTS.md`, `server/CONSTRAINTS.md`, `client/CONSTRAINTS.md` |
| Auth design | `ext/auth/docs/DESIGN.md` |
| MCP Apps design | `docs/APPS_DESIGN.md` |
| Protogen design | `experimental/ext/protogen/docs/DESIGN.md` |
| Events library | `experimental/ext/events/README.md` |
| Telegram example | `experimental/telegram-events/README.md` |
| Auth examples | `examples/auth/README.md` (unified + 5 individual servers) |
| App examples | `examples/apps/` (todolist, vanilla, react — tools, elicitation, sampling, prompts) |
| Tasks library | `server/task_*.go`, `server/tasks_experimental.go` (TaskContext, TaskElicit, TaskSample, side-channel, TaskCallbacks) |
| Tasks callbacks | `server/task_callbacks.go` — per-tool GetTask/GetResult overrides for external proxy pattern |
| Tasks client | `client/tasks.go` (ToolCallAsTask, WaitForTask, GetTask, GetTaskPayload, IsToolTask, etc.) |
| Tasks gap plan | `docs/TASKS_GAP_PLAN.md` (7-phase plan vs TS SDK) |
| Tasks v2 plan | `docs/TASKS_V2_PLAN.md` (SEP-2557 — 8-phase plan, 16 conformance scenarios) |
| Tasks example | `examples/tasks/README.md` (6 tools: sync, async, failing, elicitation, sampling, external proxy) |
| Tasks conformance | `conformance/tasks/` (27 scenarios, Go + TS parity) |
| Examples overview | `examples/README.md` |
| Tasks testing | `examples/tasks/run-exercises.sh` (16 exercises), `test-side-by-side.sh`, `ts-reference-server.mjs` |
| Conformance baseline | `conformance/baseline.yml` |
| Conformance tasks | `conformance/tasks/scenarios.test.ts` (27 scenarios) |

## Conformance Status

- Server: 30/30 (40 with baseline), Auth: 14/14 (210 checks), Apps: 21, Telegram Events: 21
- Tasks v1: 27/27 (lifecycle, errors, TTL, concurrency, elicitation, sampling, progress, status, related-task meta)
- Tasks v2: 19/21 (SEP-2557 — lifecycle, error semantics, TTL seconds, requestState, cancel, no capability; v2-16/v2-17 deferred pending MRTR)
- Keycloak interop: 12/12 (valid token, tampered, scopes, PRM, WWW-Authenticate, password grant, session hijacking, public methods, token refresh)
- testall: 12/12 stages
- Auth examples: 5 persistent servers (bearer, JWT, scopes, session-binding, public-discovery)
