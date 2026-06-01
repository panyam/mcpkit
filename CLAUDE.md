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
make testconf-stateless # SEP-2575 — upstream @ MCPCONFORMANCE_STATELESS_PATH; drives examples/stateless fixture (25 pass / 1 known upstream-test fail)
make testconf-upstream-audit # Audit mcpkit against modelcontextprotocol/conformance@main → conformance/UPSTREAM_AUDIT.md (informational scenario-level pass/fail; runs in the conformance umbrella but exits 0 by design)
make refresh-conformance # Regenerate CONFORMANCE.md from upstream tier-check + traceability (issue #498). Driver: scripts/refresh-conformance.sh, renderer: tools/conformance-report.
make check-conformance-stale # CI gate — refresh + git diff --exit-code CONFORMANCE.md (issue #498). Wired into .github/workflows/test.yml on every PR.
make testall           # Everything (9 stages, 18 sub-stages) + Keycloak + HTML report
make audit             # govulncheck + gosec + gitleaks + race
make tag-push V=vX.Y.Z # Tag root + all sub-modules and push
```

## Package Layout

| Package | Docs |
|---------|------|
| `core/` — Protocol types, typed contexts, session APIs | `core/README.md`, `core/CONSTRAINTS.md` |
| `server/` — Server, transports, middleware, v1 tasks (frozen) | `server/README.md`, `server/CONSTRAINTS.md` |
| `client/` — Client, transports, reconnection, auth retry | `client/README.md`, `client/CONSTRAINTS.md` |
| `ext/auth/` — JWT, PRM, OAuth (separate go.mod) | `ext/auth/docs/DESIGN.md` |
| `ext/tasks/` — SEP-2663 v2 tasks extension (separate go.mod) | `ext/tasks/README.md` |
| `ext/ui/` — MCP Apps, Bridge JS, AppHost, ServerRegistry (separate go.mod) | `docs/APPS_DESIGN.md`, `docs/APPS_HOST.md`, `docs/APPS_ONBOARDING.md` |
| `experimental/ext/protogen/` — Proto → MCP codegen | `experimental/ext/protogen/docs/DESIGN.md` |
| `experimental/ext/events/` — MCP Events protocol | `experimental/ext/events/README.md` |
| `testutil/` — Test helpers | |
| `tests/e2e/`, `tests/keycloak/` — Integration tests | `tests/e2e/apps/README.md` |
| `examples/` — Working examples (apps, auth, tasks, tasks-v2, mrtr, list-ttl, host, elicitation, fine-grained-auth) | `examples/README.md` |

## Sub-Modules

`ext/auth/`, `ext/tasks/`, `ext/ui/`, `experimental/ext/protogen/`, `docs/site/` have separate `go.mod` — `make test` does NOT cover them. Run `make tidy-all` after touching `core/` imports. `docs/site/` is the GitHub Pages renderer (issue 508); it is a tool, not a library, and is excluded from `SUB_MODS_TO_TAG`.

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
- **`MCPCONFORMANCE_*_PATH` (per-suite, defined in `conformance/Makefile`)**: each `testconf-*` target points at its own worktree of the [`panyam/mcpconformance`](https://github.com/panyam/mcpconformance) fork because different SEPs live on different branches while their upstream PRs are still draft. Defaults (resolved relative to `conformance/Makefile`): `MCPCONFORMANCE_TASKS_V2_PATH` and `MCPCONFORMANCE_MRTR_PATH` → `../conf-template` (`feat/tasks-mrtr-extension`); `MCPCONFORMANCE_FILE_INPUTS_PATH`, `MCPCONFORMANCE_LIST_TTL_PATH`, and `MCPCONFORMANCE_AUTH_PATH` → `../conf-pending` (`pending`). One exception — `MCPCONFORMANCE_BASE_PATH` (used by `testconf-upstream-audit`) points at a clone of the real upstream [`modelcontextprotocol/conformance`](https://github.com/modelcontextprotocol/conformance) `main` branch at `../conf-upstream-main` (no panyam fork involved); the audit reads the canonical scenario set, so any fork drift would defeat its purpose. Override per-invocation when a SEP splits to its own branch waiting upstream approval. Each target fail-fasts with a remediation message if its path is missing.
- **Upstream conformance audit (`conformance/UPSTREAM_AUDIT.md`)**: regenerated by `make testconf-upstream-audit`. Snapshot of mcpkit graded against every scenario upstream currently ships, grouped by SEP. Runs in the conformance umbrella (`make -C conformance test`) and exits 0 regardless of scenario verdicts — the audit is informational, not a gate, but its inclusion in the umbrella keeps the committed snapshot fresh. Report header carries no wall-clock timestamp; the file only diffs when upstream advances, mcpkit advances, or a scenario flips status. Use the report to prioritize implementation work; each `fail` / `harness-gap` row is a follow-up. The `also-covered-by-fork` tag is hand-maintained in `scripts/conformance-audit-report.ts` — update the `FORK_OVERLAP` map when SEP-fork targets gain or lose coverage.
- **Fixture audit (`conformance/FIXTURE_AUDIT.md`)**: companion artifact to `UPSTREAM_AUDIT.md`. Where `UPSTREAM_AUDIT.md` grades mcpkit against the upstream test set, `FIXTURE_AUDIT.md` grades the fixtures/drivers/scripts that *back* those tests — verifying we pass the suite using the same API paths real mcpkit users are taught to use, not undocumented backdoors. Point-in-time snapshot (not auto-regenerated); re-audit when a new fixture lands under `examples/`, when a new typed helper obsoletes a JUSTIFIED entry, or before a release. See the doc's "Re-audit triggers" section.
- **SEP-2575 wire mode defaults are asymmetric**: server `stateless.DefaultMode = stateless.ModeDual` (additive on upgrade — every existing server gains the stateless wire on one URL); client `client.DefaultClientMode = client.ClientModeLegacyOnly` (conservative — `Adaptive` would have silently broken 11 pre-existing client tests that assume the legacy initialize handshake). Override per-deployment via constructor option (`server.WithStatelessMode(...)` / `client.WithClientMode(...)`), env var (`MCPKIT_STATELESS_MODE` / `MCPKIT_CLIENT_MODE`), or `init()` flip of the package var. The shipping client default may flip to `Adaptive` in a future major release; doc-block calls out the migration path.
- **`HandleStore[T]` is opt-in scaffolding, not a SEP-2567 contract**: SEP-2567 is design guidance only — no wire contract, no upstream conformance. Any storage a tool handler can call (Redis, SQL, sync.Map, custom RPC) satisfies the pattern. `server.HandleStore[T]` ships the typed in-memory default + interface seam; use it, replace it, or skip it — all three are equally SEP-2567-compliant. See `docs/SEP_2567_HANDLES.md`.
- **`ctx.Sample`/`ctx.Elicit` are stateless-wire forbidden** (server-initiated push doesn't exist there). Tool handlers route through MRTR via `core.NewSamplingInputRequest` / `core.NewElicitationInputRequest` + matching decoders. The legacy push API errors out with `ErrNoRequestFunc` on stateless requests by construction. Godocs on Sample/Elicit spell out the migration with worked examples.
- **SEP-2577 deprecation pass (issue 316)**: Roots, Sampling, and Logging surfaces all carry `// Deprecated:` blocks pointing at `docs/SEP_2577_DEPRECATIONS.md`. v0.3.x keeps every API working — *no behavior change*; only `staticcheck SA1019` warnings fire at call sites. Removal target is v0.4. Affected public symbols: `core.RootsListResult`, `IsPathAllowed`, `AllowedRoots`, `SetAllowedRoots`, `BaseContext.IsPathAllowed/AllowedRoots`, `NewListRootsInputRequest`, `DecodeListRootsInputResponse`, `server.WithAllowedRoots`, `server.WithRootsFetchTimeout`, `client.RootsHandler`/`WithRootsHandler`/`Client.NotifyRootsChanged`; `core.SamplingMessage`, `ModelHint`, `ModelPreferences`, `SamplingMeta`, `CreateMessageRequest`, `CreateMessageResult`, `ErrSamplingNotSupported`, `Sample`, `BaseContext.Sample`, `NewSamplingInputRequest`, `DecodeSamplingInputResponse`, `server.TaskContext.TaskSample`, `client.SamplingHandler`/`WithSamplingHandler`; `core.LogLevel` + constants, `LogMessage`, `EmitLog`, `BaseContext.EmitLog`, `ParseLogLevel`, `MCPLogHandler` + `MCPLogHandlerOptions` + `NewMCPLogHandler` + `SlogToMCPLevel` + `MCPToSlogLevel`.
- **Handler return ABI is sealed-interface (issue #486 / PR #487)**: `ToolHandler` returns `(core.ToolResponse, error)`; `PromptHandler` returns `(core.PromptResponse, error)`. Concrete `ToolResponse` variants are `ToolResult` (sync), `InputRequiredResult` (MRTR), `CreateTaskResult` (SEP-2663 task envelope), `GoAsyncResult` (in-process spawn signal). `core.ToolResult` no longer carries `IsInputRequired`/`InputRequests`/`GoAsync` sentinel fields — they live on dedicated variant types. `ctx.RequestInput` returns `(core.InputRequiredResult, error)`. Handler bodies usually don't change: `return core.ToolResult{...}, nil` still compiles. Use `core.TypedTool[X, core.ToolResponse]` for handlers that return polymorphic variants. Migration recipe: `docs/HANDLER_RETURNS_MIGRATION.md`.
- **Stateless-wire MRTR is currently broken** (pre-existing, not caused by PR 487): `server/stateless_backend.go::callToolForStateless` decodes only `name` + `arguments` from the envelope, dropping `inputResponses` and `requestState`. So MRTR multi-round flows work on the legacy/streamable wire but fail on the stateless wire. The fork's `mrtr/all-scenarios.test.ts` stateless variant fails accordingly. `testconf-mrtr` runs both wires by default; pin `MCP_WIRE_MODES=legacy` to gate just what mcpkit fully supports today. Tracked for a follow-up fix.

Module-specific gotchas live in their READMEs.

## Conformance

Server 30/30, Auth 14/14, Apps 21, Tasks v1 27/27, Tasks v2 8 classes / ~33 checks (SEP-2663, fork), MRTR 1 class / 7 + 1 skip (SEP-2322, fork), List-TTL 5/5 (SEP-2549), File-Inputs 7/7 (SEP-2356), Stateless 25/26 (SEP-2575; 1 known upstream-test bug — array-vs-object `requiredCapabilities`), Keycloak 12/12, testall 9/9 logical stages. Tasks v2 + MRTR live in the upstream-portable [`panyam/mcpconformance`](https://github.com/panyam/mcpconformance) fork; SEP-2575 stateless lives in upstream-main directly (no fork needed). See CAPABILITIES.md `mcp-tasks-v2-conformance` and `mcp-stateless-wire`.

## Tasks v1 vs v2

Two surfaces, two entry points: `server.RegisterTasksV1` (frozen) and `tasks.Register` (v2/SEP-2663, canonical, in `ext/tasks/`). See [`docs/TASKS_V2_MIGRATION.md`](docs/TASKS_V2_MIGRATION.md).
