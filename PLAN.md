# Plan: SEP-2322 (MRTR) + SEP-2663 (Tasks Extension)

**Issues:** mcpkit#320 (SEP-2663), mcpkit#321 (SEP-2322)
**Branch:** `feat/tasks-extension` (from main)
**Approach:** Evolve v2 in place (Option B). No parallel files.

## Key decisions

- **MRTR types in core/** — SEP-2322 is accepted (10/10 vote). Track with `// SEP-2322` / `// SEP-2663` comment annotations for grep-able audit when specs finalize.
- **No `_Ext` suffix** — v2 types become THE types: `CreateTaskResult`, `DetailedTask`, `UpdateTaskRequest`, etc.
- **Types stay in core/, handlers in server/** — tasks is too deeply woven into dispatch/middleware for ext/tasks. Wire protocol says extension; implementation is core.
- **v1 stays frozen** — rename `tasks_experimental.go` → `tasks_v1.go`, keep `RegisterTasksV1()`. 27/27 conformance preserved.
- **v2 evolves** — `tasks_v2.go` and `core/task_v2.go` get updated in place to match SEP-2663.
- **Client evolves** — `client/tasks.go` updated to new shapes. No v1 client preserved (conformance tests are the safety net).

## Constraints

| Constraint | Status |
|------------|--------|
| C1 (typed contexts) | OK — `TaskContext` already typed |
| C2 (consolidated entry structs) | OK — `taskEntry` consolidated |
| C3 (no global mutable state) | OK — scoped to `v2TaskRuntime` |
| server/C4 (no spec extensions without WG) | OK — implementing Agents WG spec |

## Phase 1: MRTR base types

**Files:** `core/task_v2.go` (modify)

- [ ] Add `ResultType` constants: `ResultTypeComplete = "complete"`, `ResultTypeIncomplete = "incomplete"` (keep `ResultTypeTask = "task"`)
- [ ] Add `InputRequest` struct: `Method string`, `Params json.RawMessage` // SEP-2322
- [ ] Add type aliases: `InputRequests = map[string]InputRequest`, `InputResponses = map[string]json.RawMessage`
- [ ] Ensure `RequestState` field exists on result types // SEP-2322

**Test:** JSON marshal/unmarshal round-trip for all `ResultType` values, `InputRequest` shapes.

## Phase 2: SEP-2663 task types

**Files:** `core/task_v2.go` (evolve)

- [ ] `CreateTaskResult` — remove `Result`, `Error`, `InputRequests` fields (MUST NOT carry these per SEP-2663)
- [ ] `DetailedTask` discriminated union types: `WorkingTask`, `InputRequiredTask`, `CompletedTask`, `FailedTask`, `CancelledTask`
- [ ] `GetTaskResult` — returns `DetailedTask`
- [ ] `UpdateTaskRequest` (new): `TaskID`, `InputResponses`, `RequestState` // SEP-2663
- [ ] `UpdateTaskResult` — empty ack
- [ ] `CancelTaskResult` — empty ack (no task state)
- [ ] Wire fields: `TTLSeconds *int`, `PollIntervalMilliseconds *int` (renamed per pja-ant feedback)
- [ ] Remove `ParentTaskID` (not in SEP-2663)
- [ ] Keep internal TTL storage as ms, convert at wire boundary

**Test:** JSON shapes match SEP-2663 examples exactly (wire-format comparison against spec examples).

## Phase 3: Extension capability negotiation

**Files:** `core/session.go`, `server/dispatch.go`

- [ ] Register `io.modelcontextprotocol/tasks` as extension via `caps.Extensions`
- [ ] Remove `TasksCap`/`ServerCapabilities.Tasks` for extension path (v1 keeps its own)
- [ ] Gate task creation on `ClientSupportsExtension(ctx, "io.modelcontextprotocol/tasks")`
- [ ] Remove per-tool `ToolExecution.TaskSupport` gating — server decides unilaterally
- [ ] Support per-request capabilities via `_meta.io.modelcontextprotocol/clientCapabilities` (SEP-2575 pattern)

**Test:** Server returns `-32601` for `tasks/*` when extension not negotiated. Server MUST NOT return `CreateTaskResult` without extension.

## Phase 4: Server handlers

**Files:** `server/tasks_v2.go` (evolve)

- [ ] Rename `RegisterTasksV2` → `RegisterTasks` (THE registration)
- [ ] `tasks/get` → pure idempotent read, returns `DetailedTask`
- [ ] `tasks/update` (new handler) → accepts `InputResponses`, returns empty ack
- [ ] `tasks/cancel` → returns empty ack (not task state)
- [ ] Server-directed task creation: middleware creates task for ANY tool when server decides
- [ ] `resultType: "task"` discriminator on `CreateTaskResult`
- [ ] Set `Mcp-Name` header to `taskId` in HTTP transport for routing // SEP-2243

**Also:**
- [ ] Rename `tasks_experimental.go` → `tasks_v1.go`
- [ ] Rename `RegisterTasks` (old v1) → `RegisterTasksV1`

**Test:** Unit tests for each handler, error cases, ack-only responses.

## Phase 5: inputRequests/inputResponses flow

**Files:** `server/task_session.go` (modify), `server/tasks_v2.go`

Replace v1 side-channel pattern with poll-based input:

- [ ] `TaskContext.TaskElicit()`:
  1. Generate stable key (monotonic counter per task, e.g. `"elicit-1"`)
  2. Store `ElicitationRequest` in `taskEntry.pendingInputRequests`
  3. Transition task to `input_required`
  4. Block on response channel keyed by request key
- [ ] `TaskContext.TaskSample()`: same pattern for `sampling/createMessage`
- [ ] `tasks/get` handler: read `pendingInputRequests` → populate `inputRequests` in response
- [ ] `tasks/update` handler: match `inputResponses` keys → deliver to waiting goroutine
- [ ] Key uniqueness: monotonic counter, never reused over task lifetime

**Test:** Integration test: `confirm_delete` tool → poll → `input_required` with `inputRequests` → `tasks/update` → poll → `completed`.

## Phase 6: Client

**Files:** `client/tasks.go` (evolve)

- [ ] Remove `ToolCallAsTask()` (no client opt-in)
- [ ] `GetTask()` → returns `DetailedTask` with inlined result/error/inputRequests + `requestState`
- [ ] `UpdateTask(taskId, inputResponses, requestState)` (new)
- [ ] `CancelTask()` → returns empty (no task state)
- [ ] Remove `GetTaskPayload()` (`tasks/result` gone)
- [ ] Remove `ListTasks()` (`tasks/list` gone)
- [ ] `WaitForTask()` → poll loop, respects `PollIntervalMilliseconds`, echoes `requestState`
- [ ] Handle polymorphic `tools/call` result: check `resultType` field

**Test:** Mock server returning each result shape, polymorphic dispatch.

## Phase 7: Example server + conformance

**Files:** `examples/tasks-v2/` (evolve), `conformance/tasks-v2/` (evolve)

Example server tools:
- `greet` — sync, always returns `CallToolResult`
- `slow_compute` — async, returns `CreateTaskResult`, completes after delay
- `failing_job` — async, transitions to `failed` (protocol error)
- `confirm_delete` — async, transitions to `input_required`, resumes via `tasks/update`

Conformance evolution (from mcpkit#320 comment):
- Keep passing scenarios, adjust shapes (ack-only cancel, etc.)
- Rework v2-11 → extension negotiation
- Rework v2-16/v2-17 → `tasks/update` flow (currently unimplemented)
- Add new: ext negotiation gate, polymorphic result, Mcp-Name header
- Keep v1 suite frozen alongside

## Phase 8: Backward compat bridge

- [ ] `RegisterTasksV1()` stays for `2025-11-25` clients
- [ ] `RegisterTasks()` (new) for extension-based clients
- [ ] Both can coexist — dispatch based on negotiated capability
- [ ] Document migration path in README

## Implementation order

```
Phase 1 (MRTR types) → Phase 2 (task types) → Phase 3 (capability) →
Phase 4 (server handlers) → Phase 5 (inputRequests flow) →
Phase 6 (client) → Phase 7 (example + conformance) → Phase 8 (bridge)
```

## Deferred cleanup

- **`TaskInfoV2` → `TaskInfo` rename** (deliberately deferred during Phase 2).
  Rationale: the existing `TaskInfo` (in `core/task.go`) is *not* strictly a
  v1 wire type — it's the internal `TaskStore` record (`server/task_store.go`,
  `server/task_session.go`) that happens to also serialize as the v1 wire
  shape. Renaming it to `TaskInfoV1` would conflate "internal storage" with
  "v1 protocol shape" and stop being true the moment the store record needs
  fields the wire doesn't expose. Revisit when:
  - the v1 path is removed (then `TaskInfoV2` can simply become `TaskInfo`), or
  - the store record needs to diverge from the v1 wire shape (then introduce
    a dedicated `taskRecord` type and free up `TaskInfo` for the v2 wire).

- **Internal v2 symbols keep `V2`/`v2` infix** (deferred during Phase 4).
  After promoting `RegisterTasksV2` → `RegisterTasks` and `TasksV2Config` →
  `TasksConfig`, the canonical-named v2 file still has internal symbols like
  `taskV2Middleware`, `v2TaskRuntime`, `newV2TaskRuntime`, `makeV2GetHandler`,
  `notifyV2TaskStatus`, `notifyV2TaskStatusFromInfo`, `toTaskInfoV2`. They
  collide with v1 internals in the same package (`taskMiddleware`,
  `taskRuntime`, `makeGetHandler`, `notifyTaskStatus`), so dropping the V2
  prefix requires renaming v1 internals to `*V1` first. Pure stylistic
  cleanup with no user-visible impact — sweep when v1 is removed or when a
  refactor already touches these files.

## Open spec questions (watch before finalizing)

1. `requestState` rejection: synchronous `-32602` or silent? (Luca TBD)
2. SEP-2575 per-request capabilities shape — may change
3. `tasks/update` / `tasks/cancel` ack for unknown taskId — may add optional validation errors

## Reference

- SEP-2663 PR: https://github.com/modelcontextprotocol/specification/pull/2663
- SEP-2322 PR: https://github.com/modelcontextprotocol/specification/pull/2322
- SEP-2663 analysis: mcpkit#320
- Existing v2 PR: mcpkit#301 (19/21 conformance)
- Conformance test evolution: mcpkit#320 comment
