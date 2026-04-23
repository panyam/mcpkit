# Tasks V2 Plan (SEP-2557)

Tracking issue: panyam/mcpkit#296
SEP: https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2557
Related SEPs:
- SEP-2322: Multi Round-Trip Requests (MRTR) — accepted 2026-04-20
- SEP-2260: Server requests associated with client requests
- SEP-2567: Sessionless MCP via Explicit State Handles
- SEP-2549: TTL units alignment (seconds, not milliseconds)

Status: SEP-2557 is **Draft**, targeted for **2026-06-30-RC** milestone.
Deadline: Conformance test suite by **2026-05-15** (per maintainer guidance).

TS SDK reference: `~/projects/typescript-sdk`
- Branch `origin/fweinberger/tooltask-handlers` — v1 fix only (getTask/getTaskResult dispatch)
- No v2 implementation exists in TS SDK yet — we would be **first**

## V1 vs V2 Protocol Comparison

| Aspect | V1 (spec 2025-11-25) | V2 (SEP-2557) |
|--------|----------------------|---------------|
| Task creation | Client sends `task` param in tools/call | Server decides unilaterally; can return CreateTaskResult even without client hint |
| `tasks/get` | Returns TaskInfo only | Returns full state: status + inlined result/error/inputRequests |
| `tasks/result` | Separate blocking long-poll method | **Removed** — consolidated into `tasks/get` |
| `tasks/list` | Session-scoped task list | **Removed** — sessions going away (SEP-2567) |
| Capabilities | Negotiated via `TasksCap` in initialize | **Removed** — tasks are core protocol |
| `execution.taskSupport` | Per-tool field (forbidden/optional/required) | **Removed** — simplified |
| Elicitation mid-task | Side-channel via `tasks/result` SSE long-poll | Inline `inputRequests`/`inputResponses` in `tasks/get` (MRTR model) |
| Sampling mid-task | Side-channel via `tasks/result` SSE long-poll | Same MRTR model |
| `tasks/cancel` | Optional | **Required** (cooperative cancellation) |
| TTL units | Milliseconds | **Seconds** (per SEP-2549) |
| `requestState` | N/A | Opaque server-provided state, echoed by client in subsequent requests |
| Status notifications | `notifications/tasks/status` with TaskInfo | Same, but with `DetailedTask` types (inlined result/error/inputRequests) |
| `Mcp-Name` header | N/A | Required on `tasks/get` and `tasks/cancel` for routing |

## Key Schema Changes

### Task Object — New Discriminated Types

V2 introduces subtypes of Task based on status:

```
Task (base)
├── InputRequiredTask  — has inputRequests[]
├── CompletedTask      — has result (ToolResult inlined)
└── FailedTask         — has error (inlined)
```

The `tasks/get` response returns the appropriate subtype based on current status.

### tasks/get — Consolidated Response

V1:
```json
// tasks/get returns:
{"task": {"taskId": "...", "status": "working", ...}}

// tasks/result (separate method) returns:
{"content": [...], "_meta": {"io.modelcontextprotocol/related-task": {"taskId": "..."}}}
```

V2:
```json
// tasks/get returns everything:
{
  "taskId": "...",
  "status": "completed",
  "result": {"content": [...]},      // inlined when completed
  "requestState": "opaque-server-state"
}

// Or for input_required:
{
  "taskId": "...",
  "status": "input_required",
  "inputRequests": [{"method": "elicitation/create", ...}],
  "requestState": "opaque-server-state"
}
```

### tasks/cancel — Now Required + requestState

```json
// Request
{"method": "tasks/cancel", "params": {"taskId": "..."}}

// Response includes requestState
{"taskId": "...", "status": "cancelled", "requestState": "..."}
```

### requestState Flow

1. Server returns `requestState` in `tasks/get` and `tasks/cancel` responses
2. Client echoes `requestState` in subsequent `tasks/get` and `tasks/cancel` requests
3. Enables stateless server deployments (Step Functions, load-balanced)
4. "Last writer wins" semantics for parallel `tasks/get` calls

### notifications/tasks/status — DetailedTask

Terminal status notifications now include inlined results:
```json
{
  "method": "notifications/tasks/status",
  "params": {
    "taskId": "...",
    "status": "completed",
    "result": {"content": [...]}
  }
}
```

## Architecture: V1 and V2 Coexist

```
server/
├── tasks_experimental.go     ← v1 (current, spec 2025-11-25)
├── tasks_v2.go               ← v2 (SEP-2557)
├── task_callbacks.go          ← shared: TaskCallbacks (per-tool overrides)
├── task_store.go              ← shared: TaskStore (both versions)
├── task_store_v2.go           ← v2 additions (requestState, inlined results)
��── task_session.go            ← v1: TaskContext (side-channel model)
├── task_session_v2.go         ← v2: TaskContextV2 (MRTR model)
└── task_queue.go              ← v1 only: message queue (v2 doesn't need it)

client/
├── tasks.go                   ← v1 helpers
└── tasks_v2.go                ← v2 helpers (tasks/get returns everything)

core/
├── task.go                    ← shared types + v2 DetailedTask types

conformance/
├── tasks/scenarios.test.ts    ← v1 conformance (9+ scenarios)
└── tasks-v2/scenarios.test.ts ← v2 conformance (new scenarios)

examples/tasks/
├── main.go                    ← supports both v1 and v2 (flag: --tasks-version)
└── ts-reference-server.mjs    ← same
```

### Registration

```go
// v1 (current)
server.RegisterTasks(server.TasksConfig{Server: srv})

// v2 (new)
server.RegisterTasksV2(server.TasksV2Config{Server: srv})
```

### What's Shared

- `TaskStore` interface (v2 adds `SetRequestState`/`GetRequestState`)
- `InMemoryTaskStore` (TTL, terminal guards)
- `TaskCallbacks` (per-tool getTask/getResult overrides)
- `core.TaskInfo`, `core.TaskStatus`, `core.CreateTaskResult`
- `taskRuntime` (active task tracking, cancel propagation)

### What's V2-Only

- Inlined result/error/inputRequests in `tasks/get` response
- `requestState` field on requests and responses
- `DetailedTask` types (`CompletedTask`, `FailedTask`, `InputRequiredTask`)
- MRTR-based elicitation (no side-channel queue)
- No `tasks/result` handler
- No `tasks/list` handler
- No capability advertisement (tasks are core)
- TTL in seconds (not milliseconds)
- `Mcp-Name` header requirement

## Implementation Phases

### Phase V2-1: Core Types + tasks/get Handler
**Goal**: Basic v2 task lifecycle — create, poll, complete.

- [ ] **V2-1a.** `core/task.go`: `DetailedTask` types (CompletedTask, FailedTask, InputRequiredTask)
- [ ] **V2-1b.** `core/task.go`: `RequestState` field on `GetTaskResult`
- [ ] **V2-1c.** `server/task_store_v2.go`: `requestState` storage on taskEntry
- [ ] **V2-1d.** `server/tasks_v2.go`: `RegisterTasksV2()` + `TasksV2Config`
- [ ] **V2-1e.** `server/tasks_v2.go`: `tasks/get` handler — returns inlined result/error for terminal tasks
- [ ] **V2-1f.** `server/tasks_v2.go`: `tasks/cancel` handler — cooperative, required
- [ ] **V2-1g.** No `tasks/result`, no `tasks/list`, no capability advertisement
- [ ] **V2-1h.** TTL in seconds (not milliseconds)
- [ ] **V2-1i.** Tests: basic lifecycle, inlined results, cancel

### Phase V2-2: Server-Directed Task Creation
**Goal**: Server returns CreateTaskResult without client task hint.

- [ ] **V2-2a.** Middleware: always return CreateTaskResult for task-capable tools (no `task` param needed)
- [ ] **V2-2b.** Server can return immediate result even when task was created
- [ ] **V2-2c.** Tests: unsolicited task creation, immediate result shortcut

### Phase V2-3: MRTR Elicitation/Sampling (inputRequests/inputResponses)
**Goal**: Mid-task elicitation via inline model, not side-channel.

- [ ] **V2-3a.** `TaskContextV2` with `RequestInput()` — sets status to `input_required`, stores `inputRequests`
- [ ] **V2-3b.** `tasks/get` returns `inputRequests` when `input_required`
- [ ] **V2-3c.** Client sends `inputResponses` in next `tasks/get` call
- [ ] **V2-3d.** Handler receives `inputResponses`, resumes work
- [ ] **V2-3e.** Tests: elicitation round-trip, sampling round-trip

### Phase V2-4: requestState
**Goal**: Stateless server support.

- [ ] **V2-4a.** `requestState` returned in `tasks/get` and `tasks/cancel` responses
- [ ] **V2-4b.** Client echoes `requestState` in subsequent requests
- [ ] **V2-4c.** Server uses `requestState` for routing (opaque passthrough)
- [ ] **V2-4d.** Tests: requestState round-trip, last-writer-wins

### Phase V2-5: Status Notifications with DetailedTask
**Goal**: Terminal notifications include inlined results.

- [ ] **V2-5a.** `notifications/tasks/status` uses DetailedTask for terminal states
- [ ] **V2-5b.** Non-terminal notifications remain lightweight
- [ ] **V2-5c.** Tests: notification payloads for completed/failed/input_required

### Phase V2-6: Client Helpers
**Goal**: Client auto-detects and handles v2 responses.

- [ ] **V2-6a.** `client/tasks_v2.go`: `CallToolV2()` — handles CreateTaskResult, polls via tasks/get
- [ ] **V2-6b.** `WaitForTaskV2()` — polls tasks/get, extracts inlined result
- [ ] **V2-6c.** `HandleInputRequired()` — sends inputResponses
- [ ] **V2-6d.** Tests: full client-side workflow

### Phase V2-7: Conformance Suite
**Goal**: TS conformance tests against both Go and TS servers.

- [x] **V2-7a.** `conformance/tasks-v2/scenarios.test.ts` — 18 scenarios (red-before-green)
- [ ] **V2-7b.** Example server with `--tasks-version=v2` flag
- [ ] **V2-7c.** TS reference server v2 mode
- [ ] **V2-7d.** `make testconf-tasks-v2` target
- [ ] **V2-7e.** Wire format comparison (`test-side-by-side.sh` v2 mode)

### Phase V2-8: Documentation
- [ ] **V2-8a.** Update CLAUDE.md with v2 entries
- [ ] **V2-8b.** Update examples/tasks/README.md
- [ ] **V2-8c.** Update conformance/tasks/README.md → tasks-v2
- [ ] **V2-8d.** Update docs/ARCHITECTURE.md

## V2 Conformance Scenarios (Phase V2-7)

| # | Scenario | What it tests |
|---|----------|---------------|
| 01 | Sync tool call (no task) | Tool returns immediately, no CreateTaskResult |
| 02 | Server-directed task creation | Server returns CreateTaskResult without client hint |
| 03 | tasks/get returns working status | Poll non-terminal task |
| 04 | tasks/get returns completed + inlined result | Terminal poll returns result |
| 05 | tasks/get returns failed + inlined error | Failed task returns error inline |
| 06 | tasks/cancel (cooperative) | Cancel returns cancelled status |
| 07 | tasks/cancel on terminal task | Error: can't cancel completed/failed |
| 08 | TTL in seconds | Verify TTL field is seconds, not ms |
| 09 | requestState round-trip | Server returns state, client echoes |
| 10 | inputRequests via tasks/get | input_required status + inputRequests |
| 11 | inputResponses via tasks/get | Client sends responses, task resumes |
| 12 | Status notification with DetailedTask | Terminal notification includes result |
| 13 | No tasks/result method | Server rejects tasks/result call |
| 14 | No tasks/list method | Server rejects tasks/list call |
| 15 | No capabilities advertisement | tasks not in initialize.capabilities |
| 16 | Immediate result shortcut | Server returns result directly (skip polling) |

## TS SDK Reference (Research Snapshot, 2026-04-21)

### Key files on `origin/fweinberger/tooltask-handlers`:
- `packages/core/src/shared/taskManager.ts` — Central orchestration (862 lines)
  - `requestStream()` — AsyncGenerator: task creation → poll → result
  - `handleGetTask()` — checks overrides first, falls through to TaskStore
  - `handleGetTaskPayload()` — long-poll with queue draining + override dispatch
  - `extractInboundTaskContext()` — builds TaskContext from request
  - `wrapSendRequest()` — auto-sets `input_required` status before elicit/sample
- `packages/server/src/experimental/tasks/interfaces.ts` — ToolTaskHandler interface
  - `createTask` (required), `getTask` (optional), `getTaskResult` (optional)
- `packages/server/src/experimental/tasks/mcpServer.ts` — ExperimentalMcpServerTasks
  - `_taskToTool` map: taskId → toolName (in-memory only)
  - `_dispatch()` — routes getTask/getTaskResult to per-tool handlers
  - `_installOverrides()` — hooks into TaskManager

### Design notes:
- TS SDK uses `_taskToTool` map (in-memory) — same approach we should use
- Override handlers are optional; omitting falls through to TaskStore
- `getTaskResult` mapping cleaned up only after handler resolves (not on error)
- No v2 implementation exists yet in TS SDK

## Open Questions

1. **requestState semantics**: How does the server generate/validate requestState? Is it JWT-like, or opaque blob?
2. **MRTR interaction**: How exactly do inputResponses flow back? Is it a new field on tasks/get params, or a separate method?
3. **Backward compat**: Can a server advertise both v1 and v2? Or does protocol version determine which?
4. **Mcp-Name header**: Is this transport-level or protocol-level? Does it affect stdio transport?
5. **Session removal**: SEP-2567 removes sessions — how does this affect TaskStore session isolation?

These will resolve as SEP-2557 moves from Draft to accepted. Monitor the PR for updates.
