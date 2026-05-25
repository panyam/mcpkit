# Fixture Audit

Snapshot of every fixture / driver / script behind mcpkit's conformance program, graded against the API paths a documented mcpkit user would reach for. Companion artifact to [`UPSTREAM_AUDIT.md`](UPSTREAM_AUDIT.md): that file grades mcpkit against the upstream test set, this file grades mcpkit against itself — whether the surfaces we use to *pass* those tests are the same surfaces we *recommend* to users.

Driven by [issue 456](https://github.com/panyam/mcpkit/issues/456). The audit was triggered after PR 455 surfaced that the SEP-1613 testserver fixture had been using a raw `server.Tool{}` struct registration — a public-but-undocumented path. We added `core.WithInputSchemaOverride` to give the same capability through `TypedTool`, then asked: how often else have we cut a similar corner?

## Verdict legend

- **PASS** — uses a documented public API path. No action.
- **PROMOTE** — uses an internal or undocumented path; ought to be promoted to first-class API. Follow-up issue filed.
- **MIGRATE** — public alternative exists; switch the fixture. Done here or in a sub-PR.
- **JUSTIFIED** — internal / lower-level path used deliberately for a test-only reason; rationale documented inline.

## Summary

| Verdict | Count |
|---|---:|
| PASS | 13 |
| JUSTIFIED | 3 |
| PROMOTE (resolved) | 1 — [#457](https://github.com/panyam/mcpkit/issues/457) landed `core.WithToolExecution`; affected examples migrated |
| MIGRATE | 0 |

No surface uses an undocumented path. All deviations from the typed-helper happy path are either canonical (`RegisterTool` / `RegisterResource` / `RegisterPrompt` are documented public methods), justified (TaskCallbacks needs `server.Tool{}` for architectural reasons), or resolved by a follow-up.

## Per-surface verdicts

### `cmd/testserver/conformance_tools.go` — PASS

Every tool registered via `core.TextTool[In]` / `core.TypedTool[In, Out]`. The one previous outlier (`json_schema_2020_12_tool`) was migrated in PR 455 to `core.TypedTool + core.WithInputSchemaOverride`. Evidence: `conformance_tools.go` lines 23, 30, 37, 51, 65, 86, 102, 114, 132, 151, 172, 200, 255, 271, 281, 304, 318+ — every `srv.Register(...)` call wraps a `core.TextTool` / `core.TypedTool` invocation.

### `cmd/testserver/conformance_resources.go` — PASS

Uses `srv.RegisterResource(core.ResourceDef{...}, handler)` and `srv.RegisterResourceTemplate(core.ResourceTemplate{...}, handler)`. Both are documented public methods on `*Server`. No `core.TextResource` / `core.TypedResource` typed-helper exists; the def+handler pattern IS the canonical user path. Evidence: lines 17, 36, 56.

### `cmd/testserver/conformance_prompts.go` — PASS

Uses `srv.RegisterPrompt(core.PromptDef{...}, handler)` and `srv.RegisterCompletion(...)`. Both documented public methods. No typed-helper exists for prompts; def+handler is canonical. Evidence: lines 18, 35, 58, 86, 110.

### `cmd/testserver/conformance_apps.go` — JUSTIFIED

Tool registrations use `core.TextTool` / `core.TypedTool + core.WithToolMeta(&core.ToolMeta{UI: ...})` — canonical. Two deliberate deviations, both documented inline:

1. `testUIExtension{}` is inlined as a local type (line 16) rather than imported from `ext/ui`. Rationale (inline comment): *"to avoid the root module depending on ext/ui."* The root testserver lives in the root go.mod; depending on `ext/ui` (separate go.mod) is a deliberate-no.
2. `request-fullscreen` tool uses `ctx.Notify("notifications/ui/displayMode", ...)` directly rather than `ui.RequestDisplayMode(ctx)` helper (line 97). Same rationale, same inline comment.

A real `ext/ui` user would import the package and use `ui.RequestDisplayMode`. The testserver intentionally trades that ergonomics for module-graph cleanliness. Both deviations are tagged with explanatory comments at the use site.

### `cmd/testserver/main.go` — PASS

Server constructed via `server.NewServer(core.ServerInfo{...}, server.WithListen, server.WithToolTimeout, server.WithSubscriptions, server.WithExtension, server.WithRequestLogging)`. All public option helpers. Self-test mode uses `server.NewInProcessTransport` + `core.LoggingTransport` — both public. Evidence: lines 47-65.

### `cmd/testclient/main.go` — PASS

OAuth driver uses `client.NewClient(...)`, `client.WithClientLogging`, `client.WithTokenSource`, `auth.OAuthTokenSource{...}`, and `oneauthclient.FollowRedirects(nil)` for headless OAuth. All public APIs; PR 454 (SEP-837) work was on this same surface. Evidence: lines 47, 77, 88. No bypass of discovery / PKCE / DCR — the full `OAuthTokenSource` flow is exercised exactly as `ext/auth/docs/DESIGN.md` describes.

### `examples/auth/` — PASS

Uses `server.NewServer` with documented option helpers. Tools registered via canonical paths (no raw `server.Tool{}` found in grep). Documentation block at `main.go:1-14` explicitly covers the two-process architecture from `examples/CONVENTIONS.md`.

### `examples/tasks-v2/` — JUSTIFIED

One raw `server.Tool{}` usage at `main.go` for `external_job` — needed because the tool registers `TaskCallbacks: &server.TaskCallbacks{GetTask: ..., GetResult: ...}` for the external-proxy pattern. `TaskCallbacks` is a `server.Tool` field that `core.TypedTool` cannot expose without creating a `core → server` import (wrong direction). **JUSTIFIED with no in-scope migration.**

All other tools (`slow_compute`, `failing_job`, `confirm_delete`, `multi_input`, `protocol_error_job`) migrated to `srv.Register(core.TypedTool[...](..., core.WithInputSchemaOverride(...), core.WithToolExecution(...)))` per [#457](https://github.com/panyam/mcpkit/issues/457) resolution. The typed-helper path now covers every tool with `Execution` set.

### `examples/mrtr/` — JUSTIFIED

All 7 MRTR tools register via `srv.RegisterTool(def, handler)` with the named-handler `func(ctx, req) (ToolResult, error)` shape. The handlers need raw `req.Arguments` access to inspect `inputResponses` and `requestState` for the SEP-2322 MRTR round-trip protocol — `TypedTool`'s typed handler signature (`func(ctx, In) (Out, error)`) does not expose the raw `ToolRequest`, so migration is not feasible without a new typed-helper API for MRTR-style stateful tools. **JUSTIFIED** — rationale is the handler-signature requirement, not `Execution`. None of the MRTR tools set `Execution`; they're driven by the MRTR helpers, not the v2 tasks protocol.

### `examples/list-ttl/` — PASS

`srv.RegisterTool` / `RegisterResource` / `RegisterResourceTemplate` / `RegisterPrompt` — all documented public methods. Server constructed via `server.NewServer` with `server.WithListTTLMs(...)` option (SEP-2549). No raw struct usage.

### `examples/file-inputs/` — PASS

Imports `ext/ui` and uses `ui.RequestDisplayMode` / `ui.FileInputAnnotation` helpers (the documented `ext/ui` surface). `srv.RegisterTool` for SEP-2356 file-input tools. No raw struct usage.

### `examples/tasks/` — JUSTIFIED

Same shape as `examples/tasks-v2/`: one raw `server.Tool{TaskCallbacks: ...}` for the `external_job` proxy pattern (JUSTIFIED, same reason). All other tools (`slow_compute`, `failing_job`, `confirm_delete`, `write_haiku`) migrated to `core.TypedTool` + `WithInputSchemaOverride` + `WithToolExecution` per [#457](https://github.com/panyam/mcpkit/issues/457) resolution.

### `scripts/conformance-audit.sh` — PASS

Spawns `cmd/testserver` via `STREAMABLE=1 PORT=$AUDIT_PORT` — exactly the env-var pattern `cmd/testserver/main.go`'s header documents (`STREAMABLE=1 go run ./cmd/testserver`). Builds `cmd/testclient` via standard `go build`. Drives the upstream CLI from a real source checkout. No internal flags.

### `scripts/conformance-test.sh` — PASS

Same `STREAMABLE=1 PORT=$PORT go run ./cmd/testserver` spawn pattern. Documented surface.

### `scripts/conformance-auth-test.sh` — PASS

Builds `cmd/testclient` via `go build -buildvcs=false`. Invokes upstream CLI's `client --suite auth --command` with the built binary. testclient itself was audited above as PASS.

## TaskCallbacks documentation gap (note, not a verdict)

`server.Tool{TaskCallbacks: ...}` is the only path for tools that need per-tool `GetTask` / `GetResult` overrides (external-job proxy pattern). It's documented in `server/registration.go`'s `Tool` struct comment, but a `core/README.md` (or `server/README.md`) "When to reach for `server.Tool{}` directly" section would surface the pattern more discoverably. Optional follow-up; not filing as PROMOTE since the API is correct, only discoverability is at issue.

## Re-audit triggers

`FIXTURE_AUDIT.md` is a point-in-time snapshot, not auto-regenerated. Re-audit when:

- A new fixture binary lands under `examples/`.
- A new typed helper lands in `core/` that obsoletes a JUSTIFIED entry (e.g., when [#457](https://github.com/panyam/mcpkit/issues/457) lands, the `examples/tasks*` `RegisterTool` JUSTIFIED entries graduate to MIGRATE → PASS).
- Before each major release.
- Any time a new conformance suite (testconf-*) wires up a new fixture binary.

Optional automation: a static-analysis Go test that greps for `server.Tool{` / `server.Resource{` literal usage in `cmd/testserver/` and `examples/` and fails on unexpected matches, with an allowlist for the documented JUSTIFIED cases. Tracked as a follow-up if drift becomes an issue.

## Methodology

For each surface: identify the registration / setup / driver pattern, compare against the canonical pattern a documented mcpkit user would reach for (per `README.md`, per-package `README.md`, `examples/CONVENTIONS.md`, public exported helpers), and assign one of the four verdicts.

Canonical paths used as reference:

| Surface | Canonical pattern |
|---|---|
| Tool registration | `srv.Register(core.TextTool[In](...))` / `srv.Register(core.TypedTool[In, Out](..., opts...))` |
| Tool with raw 2020-12 schema | `core.TypedTool[In, Out](..., core.WithInputSchemaOverride(schema))` (post PR 455) |
| Tool with `Execution` | `srv.Register(core.TypedTool[In, Out](..., core.WithToolExecution(...)))` (post [#457](https://github.com/panyam/mcpkit/issues/457)) |
| Tool with `TaskCallbacks` | `srv.Register(server.Tool{ToolDef, Handler, TaskCallbacks})` (no typed helper — server-level concept) |
| Resource registration | `srv.RegisterResource(core.ResourceDef{...}, handler)` |
| Resource template | `srv.RegisterResourceTemplate(core.ResourceTemplate{...}, handler)` |
| Prompt registration | `srv.RegisterPrompt(core.PromptDef{...}, handler)` |
| Server construction | `server.NewServer(core.ServerInfo{...}, server.With...())` |
| Client construction | `client.NewClient(url, core.ClientInfo{...}, client.With...())` |
| OAuth client driver | `auth.OAuthTokenSource{...}` from `ext/auth` |
| Spawn flags in scripts | `STREAMABLE=1` / `STDIO=1` / `BOTH=1` / `PORT` (per `cmd/testserver/main.go` header docs) |
