# Plan: SEP-2663 conformance alignment

**Context:** External review of tasks v2 conformance suite found structural
wire-format mismatches and coverage gaps against the published SEP-2663 text.
The implementation works end-to-end but the wire shapes diverge from spec in
two places, and several spec requirements lack test coverage.

## Must fix (structural — wire format breaks against spec-compliant client)

### Fix 0: Revert result_type → resultType (camelCase)

Luca confirmed camelCase is the spec standard. We had renamed to snake_case
based on upstream conformance PR 188, but that PR is also wrong — Luca will
fix it on their side.

Files changed:

- [x] `core/task_v2.go` — every `json:"result_type"` tag → `json:"resultType"`.
- [x] `core/tool.go` — `ToolResult.ResultType` MarshalJSON tag + UnmarshalJSON
  alias tag.
- [x] `core/task_v2_test.go` — all `m["result_type"]` map reads + the
  IncompleteResult negative assertion now checks `m["result_type"]` is absent
  (since camelCase IS the wire field, snake_case must NOT leak).
- [x] `server/tasks_v2_test.go`, `server/mrtr_test.go`, `server/tasks_hybrid_test.go`,
  `server/tasks_v2.go` — all assertions and comments.
- [x] `conformance/tasks-v2/scenarios.test.ts`, `conformance/mrtr/scenarios.test.ts`
  — all `result.result_type` reads + literal `{ result_type: 'complete' }` ack
  comparisons + helper functions.
- [x] `client/tasks.go` — `parseToolCallResult` probe field tag.
- [x] `client/tasks_test.go`, `client/mrtr_test.go`, `client/mrtr.go` — comments.
- [x] Examples (`examples/tasks-v2/walkthrough.go` + WALKTHROUGH.md + README.md;
  `examples/mrtr/walkthrough.go` + WALKTHROUGH.md + README.md;
  `examples/README.md`) — narration.
- [x] Docs and stack artifacts (`docs/TASKS_V2_MIGRATION.md`, `CAPABILITIES.md`,
  `conformance/tasks-v2/README.md`, `conformance/mrtr/README.md`).
- [x] `CLAUDE.md` — gotcha bullet about `result_type` snake_case removed (no
  longer true).

**Test:** `grep -rn 'result_type' --include='*.go' --include='*.ts' --include='*.md'`
across the project returns zero matches except in `tests/reports/` (stale logs)
and intentional history comments (`core/task_v2.go`, `core/task_v2_test.go`
negative assertion, `conformance/mrtr/scenarios.test.ts`,
`conformance/mrtr/README.md`, this plan).

### Fix 1: Flatten CreateTaskResult

SEP-2663 defines `CreateTaskResult = Result & Task` — a flat intersection where
`taskId`, `status`, `ttlSeconds`, etc. sit at the top level alongside `result_type`.

**Current (wrong):**
```json
{"result_type": "task", "task": {"taskId": "...", "status": "working", "ttlSeconds": 60}}
```

**Expected (spec):**
```json
{"result_type": "task", "taskId": "...", "status": "working", "ttlSeconds": 60}
```

Upstream conformance PR 188 reads `result.taskId` (flat), not `result.task.taskId`.

Files to change:

- [x] `core/task_v2.go` — `CreateTaskResult` now embeds `TaskInfoV2` directly,
  so `encoding/json` promotes the task fields to the parent (same trick
  `DetailedTask` already used). No custom `MarshalJSON`/`UnmarshalJSON` needed —
  marshal and unmarshal both round-trip the flat shape automatically.
- [x] `core/task_v2_test.go` — `TestCreateTaskResultWireShape` rewritten:
  asserts `taskId` / `status` / `ttlSeconds` / `pollIntervalMilliseconds` at
  the top level, asserts no `"task"` wrapper key, plus a marshal→unmarshal
  round-trip that recovers the embedded `TaskInfoV2`.
- [x] `server/tasks_v2.go` — task-creating middleware builds
  `CreateTaskResult{ResultType: ResultTypeTask, TaskInfoV2: wireTask}`.
- [x] `client/tasks.go` — `parseToolCallResult` already just `json.Unmarshal`s
  the raw payload into the typed struct; no change needed once the struct is
  flat. (Field access on `*ToolCallResult.Task` is now `res.Task.TaskID` etc.)
- [x] `client/tasks_test.go` — `res.Task.Task.X` → `res.Task.X` everywhere
  (the `*core.CreateTaskResult` is now flat).
- [x] `conformance/tasks-v2/scenarios.test.ts` — All `result.task.X` reads
  changed to `result.X`. `assertCreateTaskResult` rewritten to assert the flat
  shape (no `task` wrapper, `taskId`+`status` at top level). Sync-tool checks
  switched to `result.taskId` / `result.result_type !== 'task'` rather than
  inspecting a wrapper that no longer exists. v2-12 reads `result.ttlSeconds`
  directly. v2-20 dispatches on `result.result_type === 'task'`.
- [x] Other callers updated: `server/tasks_v2_test.go`, `server/mrtr_test.go`,
  `server/tasks_hybrid_test.go`, `examples/tasks-v2/walkthrough.go` (+ its
  generated `WALKTHROUGH.md` mermaid arrows), `conformance/mrtr/scenarios.test.ts`
  (deferred mrtr-08 assertion), `conformance/tasks-v2/README.md`,
  `docs/TASKS_V2_MIGRATION.md`, `CAPABILITIES.md`.

**Test:** `TestCreateTaskResultWireShape` covers the round-trip; full
`make test` and `make testconf-tasks-v2` (27/27) + `make testconf-mrtr`
(7/7 + 1 skip) pass against the flat shape.

### Fix 2: Protocol version

- [ ] `conformance/tasks-v2/scenarios.test.ts` — Change `protocolVersion: '2025-11-25'`
  to `'2026-06-30'` in the `before()` block / `initRawSession` calls.

**Test:** Suite still passes with new version string.

## Should fix (coverage gaps — spec requirements without test assertions)

### Fix 3: pollIntervalMilliseconds rename assertion

Parallel to the existing `ttlSeconds` test in v2-12. Assert:
- `pollIntervalMilliseconds` key present on CreateTaskResult when server sets it
- Legacy `pollInterval` key absent

- [ ] `conformance/tasks-v2/scenarios.test.ts` — Add assertion to v2-12 or new v2-XX
- [ ] `core/task_v2_test.go` — Add wire-format test for `pollIntervalMilliseconds`

### Fix 4: requestState stale-value tolerance

SEP-2663: "Servers MUST tolerate receiving a stale or outdated value gracefully."

- [ ] `server/tasks_v2_test.go` — New test: create task, get requestState A, get again
  (requestState B), send tasks/get with stale requestState A → should succeed (not -32602).
- [ ] `conformance/tasks-v2/scenarios.test.ts` — New scenario v2-XX: echo a previously-valid
  but no-longer-latest requestState → server must accept.

Note: Our HMAC implementation currently verifies the signature is valid, not that it's the
latest value. So stale-but-valid tokens should already work. This test confirms it.

### Fix 5: Strong consistency (immediate tasks/get after create)

SEP-2663: "A server MUST NOT return CreateTaskResult until the task is durably created —
that is, until a tasks/get for the returned taskId would resolve."

- [ ] `server/tasks_v2_test.go` — New test: call tools/call → get CreateTaskResult →
  immediately call tasks/get with returned taskId → must succeed (not -32602).
- [ ] `conformance/tasks-v2/scenarios.test.ts` — New scenario v2-XX: immediate tasks/get
  after CreateTaskResult must resolve.

Note: Our implementation already does this (store.Create is synchronous before response).
This test codifies it.

### Fix 6: Mcp-Method header assertion

SEP-2243 requires both `Mcp-Name` AND `Mcp-Method` headers. v2-24 covers Mcp-Name only.

- [ ] `server/tasks_v2_test.go` — Extend Mcp-Name test to also verify `Mcp-Method` header
  is set to the JSON-RPC method name (e.g., `tasks/get`, `tasks/cancel`, `tasks/update`).
- [ ] `conformance/tasks-v2/scenarios.test.ts` — Extend v2-24 or new scenario to assert
  `Mcp-Method` header.

### Fix 7: Cleanup stale comments and v2-16

- [x] `conformance/tasks-v2/scenarios.test.ts` v2-16 — PROVISIONAL comment
  removed; replaced with a pointer to v2-17 (which exercises tasks/update).
- [ ] `conformance/tasks-v2/README.md` — Note that wire shapes match the implementation,
  not necessarily the published SEP TS declarations (which may lag behind).

## Implementation order

```
Fix 1 (flatten CreateTaskResult) — largest, touches core/server/client/conformance
  → Fix 2 (protocol version) — trivial, do alongside Fix 1
  → Fix 7 (cleanup + v2-16) — trivial, do alongside Fix 1
→ Fix 3 (pollInterval assertion) — small, independent
→ Fix 4 (stale requestState tolerance) — small, independent
→ Fix 5 (strong consistency test) — small, independent
→ Fix 6 (Mcp-Method header) — small, independent
```

Fix 1 is the bulk of the work. Fixes 3-6 are independent test additions that can be
done in parallel after Fix 1 lands.

## Not in scope (deferred)

- MRTR + tasks composition (issue 347, P2/parked — middleware redesign needed)
- Partial inputResponses fulfillment test (P2 — nice to have)
- TTL expiry post-path test (P2 — hard to test conformantly)
- Polling rate-limit test (P2 — hard to test conformantly)
- v2-08 cancel-on-terminal semantics (waiting on Luca's decision on silent-ack vs -32602)
- `result_type` vs `resultType` spec text update (flag to Luca, not our code change)
