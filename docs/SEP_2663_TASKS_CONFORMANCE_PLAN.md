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

- [ ] **Deferred — pending the actual `2026-06-30` spec release.** The
  conformance harness currently negotiates `'2025-11-25'`. Bumping requires
  a paired change to `server/dispatch.go:supportedProtocolVersions`, which
  would amount to declaring "mcpkit supports the not-yet-released
  2026-06-30 spec." Revisit when that version actually lands upstream.

## Should fix (coverage gaps — spec requirements without test assertions)

### Fix 3: pollIntervalMilliseconds rename assertion

Parallel to the existing `ttlSeconds` test in v2-12. Assert:
- `pollIntervalMilliseconds` key present on CreateTaskResult when server sets it
- Legacy `pollInterval` key absent

- [x] `conformance/tasks-v2/scenarios.test.ts` v2-12 extended to also assert
  `pollIntervalMilliseconds` (when present, must be a positive number) and
  the absence of the legacy `pollInterval` key. Title updated accordingly.
- [x] `core/task_v2_test.go` — already covered by `TestTaskInfoV2WireFields`
  (asserts both renamed keys are present and both legacy keys are absent)
  and by the rewritten `TestCreateTaskResultWireShape` (which now exercises
  `PollIntervalMilliseconds` round-trip on the flat envelope too). No new
  test needed.

### Fix 4: requestState stale-value tolerance

SEP-2663: "Servers MUST tolerate receiving a stale or outdated value gracefully."

- [x] `server/tasks_v2_test.go` — `TestV2_RequestState_StaleTolerance`: create
  task, mint tokenA, sleep ≥1s (unix-seconds expiry granularity), tasks/get
  with tokenA mints tokenB, then echo the older tokenA — server MUST accept.
- [x] `conformance/tasks-v2/scenarios.test.ts` — `v2-28` mirrors the same
  flow against any conformant server.

Confirmed: our HMAC verifier checks signature + expiry only, never "latest
version" — stale-but-valid tokens already work. The new tests lock in that
behavior so a "track latest only" refactor would fail loudly.

### Fix 5: Strong consistency (immediate tasks/get after create)

SEP-2663: "A server MUST NOT return CreateTaskResult until the task is durably created —
that is, until a tasks/get for the returned taskId would resolve."

- [x] `server/tasks_v2_test.go` — `TestV2_StrongConsistency_ImmediateGet`:
  tools/call → CreateTaskResult → immediate tasks/get (no sleep, no goroutine
  yield) — must resolve, not -32602.
- [x] `conformance/tasks-v2/scenarios.test.ts` — `v2-27` mirrors the same flow.

Confirmed: middleware calls `store.Create` synchronously before building
the response, so the task is queryable the instant the client sees the
CreateTaskResult. The new tests codify that ordering.

### Fix 6: Mcp-Method header assertion

SEP-2243 requires both `Mcp-Name` AND `Mcp-Method` headers. v2-24 covers Mcp-Name only.

- [x] `server/server.go:dispatchWithOpts` now stages `Mcp-Method: <req.Method>`
  for every JSON-RPC response via the existing `core.WithResponseHeaderCollector`
  plumbing. Pairs with the v2 task middleware's existing `Mcp-Name: <taskId>`.
- [x] `server/tasks_v2_test.go` — `TestV2_McpNameHeaderOnTaskCreation` extended
  to also assert `Mcp-Method == "tools/call"`. New `TestV2_McpMethodHeaderOnTasksMethods`
  verifies tasks/get and tasks/update responses carry the right method.
- [x] `conformance/tasks-v2/scenarios.test.ts` — v2-24 extended (Mcp-Method on
  task-creating tools/call), v2-24b extended (Mcp-Method present on sync
  tools/call too), v2-24c new (Mcp-Method on tasks/get and tasks/cancel).

### Fix 7: Cleanup stale comments and v2-16

- [x] `conformance/tasks-v2/scenarios.test.ts` v2-16 — PROVISIONAL comment
  removed; replaced with a pointer to v2-17 (which exercises tasks/update).
- [x] `conformance/tasks-v2/README.md` — Added a paragraph at the top noting
  this suite tracks spec text + mcpkit's end-to-end behavior, not the
  published SEP TypeScript declarations (which may lag — most recent
  example: the upstream conformance PR briefly used snake_case
  `result_type`, but Luca confirmed camelCase is the standard).

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

- MRTR + tasks composition (issue 347, P2/parked — middleware redesign
  needed; mrtr-08 conformance skip already exists in
  `conformance/mrtr/scenarios.test.ts`).
- MRTR resultType discriminator collision (`"input_required"` vs
  `"incomplete"`) — SEP-2322 and SEP-2663 drafts disagree on the wire
  value; awaiting alignment between the two SEP authors. Tracked on
  PR 2663 comment 4381885336 + PR 2322 comment 4381884825. mcpkit
  follows SEP-2663's `"incomplete"`. The mrtr conformance suite
  centralizes the literal in `MRTR_INCOMPLETE_RESULT_TYPE` so the
  eventual spec resolution is a single-line flip.
- Partial inputResponses fulfillment test (P2 — nice to have)
- TTL expiry post-path test (P2 — hard to test conformantly)
- Polling rate-limit test (P2 — hard to test conformantly)
- `result_type` vs `resultType` spec text update (flag to Luca, not our code change)

## Resolved (post-review)

- v2-08 cancel-on-terminal semantics — spec settled on SHOULD -32602 in
  modelcontextprotocol/specification commit d963ad0. Our -32602 is
  spec-aligned; the v2-08 test comment now points to that commit.
- assertCreateTaskResult forbidden-field validation (no result / error /
  inputRequests on the envelope) — added to the helper.
- ISO-8601 timestamp validation on createdAt + lastUpdatedAt — added to
  the helper alongside the forbidden-field rule.
- v2-30 (tasks/get with unknown taskId returns -32602) — new scenario
  mirrors v2-08 for the read path.
- v2-31 (legacy v1 `task` param ignored) — new scenario asserts servers
  tolerate a legacy hint without erroring or promoting sync tools.
- v2-29 (partial inputResponses fulfillment) — new scenario backed by a
  `multi_input` fixture that fans two parallel TaskElicits. Required a
  small server fix in `requestInputV2`: the task now stays in
  input_required when other inputs are still pending, so partial updates
  don't briefly flip status to "working" and back.
- v2-24 / v2-24b / v2-24c repurposed from response-header assertions
  (mcpkit-specific echo behavior, already covered by Go tests
  `TestV2_McpName*` / `TestV2_McpMethod*`) to request-header tolerance —
  a real SEP-2243 conformance check (server tolerates `Mcp-Method` /
  `Mcp-Name` request headers; body is authoritative). Required adding
  `opts.headers` to the raw fetch helpers so tests can attach arbitrary
  request headers.
- Conformance suite scrubbed of mcpkit-specific framing (in-repo / in-tree
  references, "mcpkit picks…" phrasing in open-spec-questions). The suite
  now reads as a brand-neutral SEP-2663 / 2322 / 2575 / 2243 server check.
