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

## Phase 2: TTL Enforcement ✅ COMPLETE
**Goal**: Tasks auto-expire after TTL.

- [x] **2a.** Timer per task on `taskEntry` — `time.AfterFunc` on Create, reset on SetResult/Cancel
- [x] **2b.** `Cleanup()` method — stops all timers, clears all tasks
- [x] **2c.** Tests: 6 unit (expiry, reset on result, reset on cancel, null no-expiry, cleanup, cleanup stops timers) + 1 e2e (exercise 9)

## Phase 3: Session Isolation ✅ COMPLETE
**Goal**: Tasks are scoped to the session that created them.

- [x] **3a.** `sessionID` on all TaskStore methods + `sessionAllowed()` check on taskEntry
- [x] **3b.** `core.SetSessionID` / `core.GetSessionID` / `BaseContext.SessionID()` — server dispatch sets it
- [x] **3c.** 5 unit tests (Get, Update, Cancel, List isolation + empty backward compat)

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

## Phase 5: Cancellation Propagation ✅ COMPLETE
**Goal**: Cancelled tasks stop their goroutines.

- [x] **5a.** `context.WithCancel` on detached context, cancel func in `activeTask` struct (C2)
- [x] **5b.** Cancel handler calls `rt.cancelTask()` → goroutine's `ctx.Done()` fires
- [x] **5c.** `TestTaskCancelStopsGoroutine` + example `slow_compute` checks `ctx.Err()`

## Phase 6: Status Notifications ✅ COMPLETE (Option 1)
**Goal**: Clients receive push notifications on status changes.

- [x] **6a.** `notifyTaskStatus()` sends `notifications/tasks/status` after status changes
- [x] **6b.** Cancel handler + tasks/result handler + TaskContext.SetStatus all notify
- [x] **6c.** `TestTaskStatusNotificationOnComplete` + `TestTaskStatusNotificationOnCancel`
- [ ] **6d.** Queue-based notification storage for replay/reliability (#288)

## Phase 7: Progress Notifications ✅ PARTIAL
**Goal**: Progress notifications flow through task lifecycle.

- [x] **7a.** `DetachForBackground` replaces notifyFunc with session-level one — progress from background goroutines reaches client via GET SSE
- [x] **7b.** Go `slow_compute` emits per-second progress (matching TS reference server)
- [ ] **7c.** Preserve original `_meta.progressToken` from `tools/call` through to task goroutine (currently uses taskId as token)
- [ ] **7d.** Clean up progress handler on terminal state

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
| `server/task_queue.go` | NEW — TaskMessageQueue + InMemoryMessageQueue |
| `server/task_queue_test.go` | NEW — 9 queue tests |
| `server/task_session.go` | NEW — TaskContext, side-channel request/response |
| `server/tasks_experimental.go` | taskRuntime, long-poll handler, side-channel proxy |
| `server/task_store.go` | Consolidated taskEntry, WaitForUpdate, context-aware WaitForResult |
| `server/task_store_test.go` | WaitForUpdate tests, context cancellation |
| `server/tasks_experimental_test.go` | 61 tests total |
| `client/tasks.go` | pollInterval in task hint |
| `examples/tasks/main.go` | confirm_delete + write_haiku tools |
| `examples/tasks/README.md` | Updated features, exercises, curl note |
| `examples/tasks/test-side-by-side.sh` | NEW — wire format comparison vs TS SDK |
