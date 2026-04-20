# Tasks Gap Closure Plan

Tracking issue: panyam/mcpkit#279
Conformance binary: panyam/mcpkit#282
Sub-task threading: panyam/mcpkit#281
TS SDK reference: `~/projects/typescript-sdk`

Key TS files:
- `packages/core/src/shared/taskManager.ts` (862 lines — central orchestration)
- `packages/core/src/experimental/tasks/interfaces.ts` (TaskStore, TaskMessageQueue, QueuedMessage)
- `packages/core/src/experimental/tasks/stores/inMemory.ts` (InMemoryTaskStore, InMemoryTaskMessageQueue)
- `packages/server/src/experimental/tasks/server.ts` (ExperimentalServerTasks)
- `examples/server/src/simpleTaskInteractive.ts` (full example with elicitation + sampling)

## Phase 1: TaskMessageQueue + input_required + tasks/result long-poll ✅ COMPLETE

**Goal**: Background tasks can do elicitation and sampling mid-execution.

- [x] **1a.** TaskMessageQueue interface (`queue.go`)
- [x] **1b.** InMemoryMessageQueue with WaitForMessage
- [x] **1c.** TaskContext with TaskElicit/TaskSample (`session.go`)
- [x] **1d.** Long-poll tasks/result loop with queue draining
- [x] **1e.** Queue cleanup on terminal state
- [x] **1f.** Config wiring (MessageQueue, MaxQueueSize)
- [x] **1g.** relatedTask metadata on ElicitationMeta/SamplingMeta + ParentTaskID
- [x] **1h.** `core.DetachForBackground(ctx)` — background-safe context with session-level push
- [x] **1i.** Queue-based side-channel delivery: TaskElicit enqueues → tasks/result handler proxies via live ctx.Elicit()
- [x] **1j.** Example: confirm_delete (elicitation) + write_haiku (sampling) tools
- [x] **1k.** Tests: 61 total — queue (9), store (13), lifecycle (17), wire format (6), e2e elicitation (1), e2e sampling (1), panic recovery (1), result semantics (2), TaskContext (2), input_required (1), concurrent cancel (1), queue cleanup (1)

**Also fixed during Phase 1:**
- [x] Panic recovery in background goroutine (`defer recover()`)
- [x] `tasks/result` returns stored ToolResult for ALL terminal states (not JSON-RPC errors)
- [x] `pollInterval` passthrough from client task hint
- [x] Context-aware `WaitForResult` and `WaitForUpdate` (respect HTTP disconnect)
- [x] Consolidated store into single `taskEntry` struct (constraint C2)
- [x] Instance-scoped `taskRuntime` (no package globals, constraint C3)

**Wire format parity verified** via `test-side-by-side.sh` against TS SDK.

## Phase 2: TTL Enforcement
**Goal**: Tasks auto-expire after TTL.

- [ ] **2a. Add cleanup timer to InMemoryStore**
  - On `Create()`: schedule cleanup with `time.AfterFunc(ttl, cleanup)`
  - On `StoreResult()`: reset timer to start from now
  - On terminal status: reset timer
  - Store timer handle alongside task in taskEntry
  - Ref: `stores/inMemory.ts:66-76, 118-131, 170-182`

- [ ] **2b. Add `Cleanup()` method**
  - Stop all timers, clear all tasks
  - For graceful shutdown and testing

- [ ] **2c. Tests** — TTL expiry, timer reset on result, cleanup on shutdown

## Phase 3: Session Isolation
**Goal**: Tasks are scoped to the session that created them.

- [ ] **3a. Add `sessionID` parameter to TaskStore interface methods**
  - `Create(info, sessionID)` — store sessionID with task
  - `Get(taskID, sessionID)` — return not-found if session mismatch
  - `Update(taskID, fn, sessionID)`
  - `Cancel(taskID, sessionID)`
  - `List(cursor, limit, sessionID)` — filter by session
  - Ref: `stores/inMemory.ts:82-93`

- [ ] **3b. Pass session ID from middleware/handler context**

- [ ] **3c. Tests** — cross-session access denied, same-session access allowed

## Phase 4: Store API Alignment
**Goal**: Match TS SDK's atomic operations and guards.

- [ ] **4a. Add `StoreResult(taskID, status, result)` atomic method**
  - Replaces separate `SetResult()` + `Update()` calls
  - Guards against storing results for terminal tasks
  - Ref: `stores/inMemory.ts:101-132`

- [ ] **4b. Add terminal state guard on `UpdateStatus()`**
  - Reject transitions from completed/failed/cancelled
  - Ref: `stores/inMemory.ts:149-160`

- [ ] **4c. Store original request + requestID with task**

- [ ] **4d. Migrate middleware to use new atomic API**

## Phase 5: Cancellation Propagation
**Goal**: Cancelled tasks stop their goroutines.

- [ ] **5a. Use `context.WithCancel` inside `DetachForBackground`**
  - Store cancel func in taskRuntime
  - `Cancel()` calls cancel func after marking status

- [ ] **5b. Tool handlers receive cancelled context**

- [ ] **5c. Tests** — cancel while running, verify goroutine exits

## Phase 6: Status Notifications
**Goal**: Clients receive push notifications on status changes.

- [ ] **6a. Wrap store with `RequestTaskStore` pattern**
  - Auto-send `notifications/tasks/status` on every status change
  - Ref: `taskManager.ts:657-712`

- [ ] **6b. Define `TaskStatusNotification` type** in `core/task.go`

- [ ] **6c. Tests** — client receives notification after status change

## Phase 7: Progress Token Tracking (P2)
**Goal**: Progress notifications flow through task lifecycle.

- [ ] **7a. Preserve progress token when CreateTaskResult is returned**
- [ ] **7b. Clean up progress handler on terminal state**

## Phase 8: Sub-Task Threading (#281)
**Goal**: Tasks can spawn sub-tasks with ParentTaskID linkage.

- [ ] **8a. `TaskContext.SpawnTool(name, args)` — creates child task**
- [ ] **8b. `TaskContext.WaitForTask(childID)` — blocks until child completes**
- [ ] **8c. Cascading cancel — cancel parent → cancel children**
- [ ] **8d. `tasks/list` parent filter**
- [ ] **8e. Example: deploy tool with parallel build + test sub-tasks**

## Verification

After each phase:
1. `cd experimental/ext/tasks && go test ./...` passes
2. Example server works with curl walkthrough
3. Wire format tests still pass
4. `bash test-side-by-side.sh` confirms parity with TS SDK

## Files Modified/Created (Phase 1)

| File | What |
|------|------|
| `core/background.go` | NEW — DetachForBackground, SetDetachStrategy, ReplaceSessionRequestFunc |
| `core/background_test.go` | NEW — 3 tests for detach strategy |
| `core/task.go` | ParentTaskID field |
| `core/task_test.go` | ParentTaskID, RelatedTask on meta types |
| `core/elicitation.go` | RelatedTask on ElicitationMeta |
| `core/sampling.go` | RelatedTask on SamplingMeta |
| `server/server.go` | DetachStrategy registration, WithMux, CloseAllSessions on shutdown |
| `experimental/ext/tasks/queue.go` | NEW — TaskMessageQueue + InMemoryMessageQueue |
| `experimental/ext/tasks/queue_test.go` | NEW — 9 queue tests |
| `experimental/ext/tasks/session.go` | NEW — TaskContext, side-channel request/response |
| `experimental/ext/tasks/tasks.go` | taskRuntime, long-poll handler, side-channel proxy |
| `experimental/ext/tasks/store.go` | Consolidated taskEntry, WaitForUpdate, context-aware WaitForResult |
| `experimental/ext/tasks/store_test.go` | WaitForUpdate tests, context cancellation |
| `experimental/ext/tasks/tasks_test.go` | 61 tests total |
| `experimental/ext/tasks/client.go` | pollInterval in task hint |
| `examples/tasks/main.go` | confirm_delete + write_haiku tools |
| `examples/tasks/README.md` | Updated features, exercises, curl note |
| `examples/tasks/test-side-by-side.sh` | NEW — wire format comparison vs TS SDK |
