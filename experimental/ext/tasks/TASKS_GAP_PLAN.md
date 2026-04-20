# Tasks Gap Closure Plan

Tracking issue: panyam/mcpkit#279
TS SDK reference: `~/projects/typescript-sdk`
Key TS files:
- `packages/core/src/shared/taskManager.ts` (862 lines — central orchestration)
- `packages/core/src/experimental/tasks/interfaces.ts` (TaskStore, TaskMessageQueue, QueuedMessage)
- `packages/core/src/experimental/tasks/stores/inMemory.ts` (InMemoryTaskStore, InMemoryTaskMessageQueue)
- `packages/server/src/experimental/tasks/server.ts` (ExperimentalServerTasks)
- `examples/server/src/simpleTaskInteractive.ts` (full example with elicitation + sampling)

## Implementation Phases

### Phase 1: TaskMessageQueue + input_required + tasks/result long-poll
**Goal**: Background tasks can do elicitation and sampling mid-execution.
**This is the biggest change — everything else builds on it.**

- [x] **1a. Define `TaskMessageQueue` interface** ✅ in `experimental/ext/tasks/queue.go`
  - `Enqueue(taskID, msg QueuedMessage, sessionID string) error`
  - `Dequeue(taskID, sessionID string) (QueuedMessage, bool)`
  - `DequeueAll(taskID, sessionID string) []QueuedMessage`
  - Message types: `QueuedRequest`, `QueuedNotification`, `QueuedResponse`, `QueuedError`
  - Each has `Type string`, `Timestamp int64`, `Message` (the JSON-RPC message)
  - Ref: `interfaces.ts:54-132`

- [x] **1b. Implement `InMemoryMessageQueue`** ✅
  - Thread-safe with mutex
  - `WaitForMessage(taskID)` — blocks via channel until enqueue (for long-poll)
  - `NotifyWaiters(taskID)` — called by Enqueue to unblock WaitForMessage
  - Ref: `stores/inMemory.ts:245-313`

- [x] **1c. Add `TaskContext` (was TaskSession)** ✅ in `experimental/ext/tasks/session.go`
  - Wraps server context for background task goroutines
  - `Elicit(message, schema)`:
    1. Transition status to `input_required`
    2. Build JSON-RPC elicitation/create request with `_meta[related-task]`
    3. Enqueue request with a response channel (Go equivalent of Resolver)
    4. Block waiting for response on channel
    5. Transition back to `working`
    6. Return response
  - `Sample(messages, maxTokens)` — same pattern with `sampling/createMessage`
  - Ref: `simpleTaskInteractive.ts:332-440`

- [x] **1d. Rewrite `tasks/result` handler as long-poll loop** ✅
  - Loop:
    1. Dequeue all messages from queue
    2. For requests (elicitation/sampling): deliver via `server.elicitInput()` / `server.createMessage()`, route response back to TaskSession channel
    3. For responses/errors: route to pending request resolvers
    4. Check if task is terminal → if yes, get result, inject `_meta[related-task]`, return
    5. Wait for update (poll interval or queue message, whichever comes first)
  - Need: `sendOnResponseStream` equivalent — write queued JSON-RPC messages onto the SSE response stream for the `tasks/result` request
  - Ref: `taskManager.ts:373-430`, `simpleTaskInteractive.ts:217-326`

- [x] **1e. Clean up queue on terminal state** ✅
  - When task reaches completed/failed/cancelled, call `DequeueAll`
  - Reject any pending request resolvers with "task cancelled/completed"
  - Ref: `taskManager.ts:816-832`

- [ ] **1f. Wire into Config and middleware**
  - Add `MessageQueue TaskMessageQueue` to `Config`
  - Default to `NewInMemoryMessageQueue()`
  - Add `MaxQueueSize int` to Config (optional, 0 = unbounded)
  - Pass queue + store to TaskSession in task goroutine

- [x] **1f. Wire into Config and middleware** ✅
  - `MessageQueue` and `MaxQueueSize` in Config
  - Queue wired into result and cancel handlers

- [ ] **1g. `relatedTask` metadata propagation**
  - All outbound requests/notifications from a task context get `_meta["io.modelcontextprotocol/related-task"]` injected
  - Ref: `taskManager.ts:478-520, 556-584`

- [ ] **1h. `core.DetachForBackground(ctx)` — background-safe context**
  - **Problem found:** The background goroutine's context carries a `requestFunc` 
    from the `tools/call` POST response, which is already closed by the time 
    TaskElicit/TaskSample runs.
  - **Solution:** `core.DetachForBackground(ctx)` — a single function that returns
    a context suitable for background goroutines. The server registers a strategy
    that replaces the dead POST requestFunc with the session-level persistent push.
  - **Implementation:**
    1. `core/background.go`: `DetachForBackground(ctx)`, `SetDetachStrategy(ctx, fn)`
       Default: `context.WithoutCancel(ctx)`. With strategy: also replaces requestFunc.
    2. `server/dispatch.go`: In `dispatchWithOpts`, set detach strategy that uses
       `d.getPushRequest()` + `d.makeRequestFunc()` for the replacement requestFunc.
    3. `experimental/ext/tasks/tasks.go`: Replace `context.WithoutCancel(ctx)` with
       `core.DetachForBackground(ctx)`.
  - **No transport changes needed.** No internal types leak.
  - **Aligns with #281** (sub-tasks): `SpawnTool` will use the same function.
  - Files: `core/background.go` (NEW), `server/dispatch.go`, `tasks/tasks.go`

- [ ] **1i. Queue-based delivery in tasks/result loop** (future, after 1h works)
  - Currently direct mode (TaskElicit → ctx.Elicit → GET SSE) works once 1h is fixed
  - Queue-based delivery would route elicitation through the tasks/result POST SSE 
    response instead — requires the tasks/result handler to write intermediate events
  - This is a separate optimization, not required for basic functionality

- [ ] **1j. Update example** — add `confirm_delete` (elicitation) and `write_haiku` (sampling) tools
  - Mirror TS SDK's `simpleTaskInteractive.ts`
  - Ref: `simpleTaskInteractive.ts:530-607`

- [ ] **1k. Tests**
  - Message queue unit tests (enqueue/dequeue/wait/cleanup) ✅
  - TaskSession elicit/sample tests
  - Long-poll integration test (create task → elicit mid-task → complete → fetch result)
  - Queue cleanup on cancel test
  - SendOnResponseStream transport test

### Phase 2: TTL Enforcement
**Goal**: Tasks auto-expire after TTL.

- [ ] **2a. Add cleanup timer to InMemoryStore**
  - On `Create()`: schedule cleanup with `time.AfterFunc(ttl, cleanup)`
  - On `StoreResult()`: reset timer to start from now
  - On terminal status: reset timer
  - Store timer handle alongside task for cancellation
  - Ref: `stores/inMemory.ts:66-76, 118-131, 170-182`

- [ ] **2b. Add `Cleanup()` method**
  - Stop all timers, clear all tasks
  - For graceful shutdown and testing

- [ ] **2c. Tests** — TTL expiry, timer reset on result, cleanup on shutdown

### Phase 3: Session Isolation
**Goal**: Tasks are scoped to the session that created them.

- [ ] **3a. Add `sessionID` parameter to TaskStore interface methods**
  - `Create(info, sessionID)` — store sessionID with task
  - `Get(taskID, sessionID)` — return not-found if session mismatch
  - `Update(taskID, fn, sessionID)`
  - `Cancel(taskID, sessionID)`
  - `List(cursor, limit, sessionID)` — filter by session
  - Ref: `stores/inMemory.ts:82-93`

- [ ] **3b. Pass session ID from middleware/handler context**
  - Extract session ID from the request context

- [ ] **3c. Tests** — cross-session access denied, same-session access allowed

### Phase 4: Store API Alignment
**Goal**: Match TS SDK's atomic operations and guards.

- [ ] **4a. Add `StoreResult(taskID, status, result)` atomic method**
  - Replaces separate `SetResult()` + `Update()` calls
  - Guards against storing results for terminal tasks
  - Ref: `stores/inMemory.ts:101-132`

- [ ] **4b. Add terminal state guard on `UpdateStatus()`**
  - Reject transitions from completed/failed/cancelled
  - Ref: `stores/inMemory.ts:149-160`

- [ ] **4c. Store original request + requestID with task**
  - Add `Request` and `RequestID` fields to stored task
  - Pass from middleware on creation
  - Ref: `interfaces.ts:183` (createTask signature)

- [ ] **4d. Migrate middleware to use new atomic API**

### Phase 5: Cancellation Propagation
**Goal**: Cancelled tasks stop their goroutines.

- [ ] **5a. Use `context.WithCancel` instead of `WithoutCancel`**
  - Store cancel func alongside task in middleware
  - `Cancel()` calls cancel func after marking status

- [ ] **5b. Tool handlers receive cancelled context**
  - `ctx.Done()` fires when task is cancelled
  - Long-running tools should check `ctx.Err()`

- [ ] **5c. Tests** — cancel while running, verify goroutine exits

### Phase 6: Status Notifications
**Goal**: Clients receive push notifications on status changes.

- [ ] **6a. Wrap store with `RequestTaskStore` pattern**
  - On every `StoreResult()` and `UpdateStatus()`:
    1. Perform the store operation
    2. Read back the updated task
    3. Send `notifications/tasks/status` with the task as params
  - On terminal state: clean up progress handler
  - Ref: `taskManager.ts:657-712` (createRequestTaskStore)

- [ ] **6b. Define `TaskStatusNotification` type** in `core/task.go`
  - Method: `notifications/tasks/status`
  - Params: flat TaskInfo fields
  - Ref: TS SDK `TaskStatusNotificationSchema`

- [ ] **6c. Tests** — client receives notification after status change

### Phase 7: Progress Token Tracking (P2)
**Goal**: Progress notifications flow through task lifecycle.

- [ ] **7a. Preserve progress token when CreateTaskResult is returned**
  - Map taskID → progressToken
  - Ref: `taskManager.ts:598-613`

- [ ] **7b. Clean up progress handler on terminal state**
  - Ref: `taskManager.ts:854-860`

## Additional Gaps Found

### Spec compliance (should fix)

- [ ] **`tasks/result` for failed/cancelled tasks returns JSON-RPC error instead of stored result**
  - Our `makeResultHandler` returns `ErrCodeToolExecutionError` (-31000) for failed tasks and `-32800` for cancelled
  - TS SDK returns the **stored result** for ALL terminal states — the result itself has `isError: true`
  - Client should get the full structured error content, not a generic JSON-RPC error
  - Fix: return stored ToolResult for all terminal states, let client check `isError` or task status
  - Ref: `taskManager.ts:416-425`

- [ ] **Context cancellation in `tasks/result` long-poll**
  - TS SDK respects `AbortSignal` during poll wait — if HTTP request is aborted, loop exits
  - Our `WaitForResult` uses `sync.Cond` with no context awareness
  - Fix: long-poll loop should select on context.Done() and the wait channel
  - Ref: `taskManager.ts:834-852`

- [ ] **Response routing for task-related server-initiated requests**
  - When a background task calls `ctx.Elicit()`, the client's response POST needs to be routed back to the task's resolver
  - TS SDK routes task-related responses through the message queue (`routeResponse` in `taskManager.ts:640-655`)
  - In our Go impl, `ctx.Elicit()` uses the GET SSE stream and blocks — this may work directly without routing, but needs verification
  - Ref: `taskManager.ts:640-655, 716-747`

### Minor (should fix)

- [ ] **Task hint `pollInterval` passthrough** — our `taskHint` struct only has `TTL`, TS SDK also passes `pollInterval` from client hint
- [ ] **Non-standard error code `-32800` for cancelled tasks** — not in JSON-RPC spec or MCP spec. TS SDK uses standard `InvalidParams` (-32602) for task not found, but returns results (not errors) for terminal tasks
- [ ] **`_meta.task` fallback** — TS SDK example checks `params._meta?.task || params.task`. Core `isTaskAugmentedRequestParams` only checks `params.task`. Our middleware matches the core behavior. Low priority.
- [ ] **Panic recovery in background goroutine** — if a tool handler panics during async execution, the goroutine dies and the task is stuck in "working" forever with no result. Need `defer recover()` to catch panics and transition to `TaskFailed` with the panic message. TS SDK handles this via try/catch.

## Verification

After each phase:
1. `cd experimental/ext/tasks && go test ./...` passes
2. Example server works with curl walkthrough
3. Wire format tests still pass
4. Compare behavior against TS SDK's `simpleTaskInteractive.ts` example

Final verification:
- Run TS SDK example and our example side by side
- Send identical curl sequences to both
- Verify identical response shapes

## Files to Modify/Create

| File | Changes |
|------|---------|
| `experimental/ext/tasks/queue.go` | NEW — TaskMessageQueue interface + InMemoryMessageQueue |
| `experimental/ext/tasks/queue_test.go` | NEW — queue unit tests |
| `experimental/ext/tasks/session.go` | NEW — TaskSession helper (elicit/sample from background) |
| `experimental/ext/tasks/session_test.go` | NEW — session tests |
| `experimental/ext/tasks/tasks.go` | Rewrite tasks/result handler, wire queue, cancellation, relatedTask |
| `experimental/ext/tasks/store.go` | TTL enforcement, session isolation, atomic StoreResult, Cleanup |
| `experimental/ext/tasks/store_test.go` | New tests for TTL, sessions, atomicity |
| `experimental/ext/tasks/tasks_test.go` | New tests for message queue, long-poll, input_required |
| `experimental/ext/tasks/client.go` | Add pollInterval to hint, minor updates |
| `core/task.go` | TaskStatusNotification type, QueuedMessage types if shared |
| `examples/tasks/main.go` | Add confirm_delete + write_haiku tools |
| `examples/tasks/README.md` | Update with new tools and exercises |
