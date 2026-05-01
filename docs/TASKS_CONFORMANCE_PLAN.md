# Plan: SEP-2663 conformance alignment

**Context:** External review of tasks v2 conformance suite found structural
wire-format mismatches and coverage gaps against the published SEP-2663 text.
The implementation works end-to-end but the wire shapes diverge from spec in
two places, and several spec requirements lack test coverage.

## Must fix (structural — wire format breaks against spec-compliant client)

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

- [ ] `core/task_v2.go` — `CreateTaskResult` struct: embed `TaskInfoV2` fields flat
  instead of nesting under `Task TaskInfoV2`. Use custom `MarshalJSON` that merges
  `ResultType` + `TaskInfoV2` fields into one flat object. Add `UnmarshalJSON` for
  round-trip.
- [ ] `core/task_v2_test.go` — Update `TestCreateTaskResultWireShape` to assert flat
  shape. Verify `taskId` at top level, no `task` wrapper key.
- [ ] `server/tasks_v2.go` — Update task creation code that builds `CreateTaskResult`.
  Fields move from `Task: info` to inline: `TaskID: info.TaskID, Status: info.Status, ...`
- [ ] `client/tasks.go` — Update `parseToolCallResult` to read flat shape
  (`result.taskId` not `result.task.taskId`).
- [ ] `client/tasks_test.go` — Update mock responses to flat shape.
- [ ] `conformance/tasks-v2/scenarios.test.ts` — Change ALL `result.task.X` reads to
  `result.X`. Affected: `assertCreateTaskResult`, v2-02, v2-03, v2-04, v2-05, v2-06,
  v2-07, v2-12, v2-13, v2-14, v2-15, v2-16, v2-17, v2-18, v2-19, v2-20, v2-21.

**Test:** Wire-format round-trip: marshal → JSON has no `"task"` key, `taskId` is top-level.
Build `CreateTaskResult`, marshal, unmarshal back, verify fields survive.

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

- [ ] `conformance/tasks-v2/scenarios.test.ts` v2-16 — Remove PROVISIONAL comment
  ("inputResponses will likely move to a separate method (TBD)"). It landed as tasks/update.
  Update v2-16 to reflect current tasks/update semantics.
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
