# Tasks Example Server

Demonstrates MCP Tasks (spec 2025-11-25) — async tool execution with lifecycle tracking.

## MCPKit Features Used

| Category | Feature |
|----------|---------|
| Core | `core.ToolDef.Execution`, `core.TaskSupportOptional`, `core.TaskSupportRequired`, `core.DetachForBackground` |
| Experimental | `experimental/ext/tasks` — `tasks.Register`, `tasks.Config`, `tasks.TaskContext`, `tasks.GetTaskContext` |
| Side-channel | `TaskContext.TaskElicit` (elicitation from background task), `TaskContext.TaskSample` (sampling from background task) |
| MCP methods | `tasks/get`, `tasks/result`, `tasks/cancel`, `tasks/list` |

## Setup

```bash
cd examples/tasks
go run . -addr :8080
```

## Connect a Host

MCPJam, VS Code, or any MCP client: `http://localhost:8080/mcp`

## Tools

| Tool | Task Support | Behavior |
|------|-------------|----------|
| `greet` | forbidden (absent) | Sync-only. Returns greeting immediately. |
| `slow_compute` | optional | Sleeps N seconds. Sync without hint, async with hint. |
| `failing_job` | required | Always fails after 1s. Must be called as a task. |
| `confirm_delete` | required + elicitation | Asks user for confirmation before deleting. |
| `write_haiku` | required + sampling | Asks the LLM to write a haiku on a topic. |

## Important: Host Support Required

MCP Tasks is an experimental protocol extension (spec 2025-11-25). **Most MCP hosts don't support it yet** — they will call tools synchronously and ignore the task hints.

Until your host supports tasks, use the [curl walkthrough](#curl-walkthrough) below to exercise the async path.

---

## Phase 1: Core Task Lifecycle ✅

These exercises work today.

### 1. Sync tool call

```
Greet World
```

Returns immediately: `Hello, World!`

### 2. Async computation

```
Run a slow computation for 5 seconds labeled "pi"
```

Returns a task ID immediately. The computation runs in the background.

### 3. Check on a running task

```
What's the status of my computation?
```

The host polls `tasks/get` — status transitions from `working` to `completed`.

### 4. Failing job

```
Run the failing job
```

This tool *requires* task invocation. The job starts, then fails after 1 second — status transitions to `failed`.

### 5. Elicitation from a task (confirm_delete)

```
Delete the file important.txt
```

The tool creates a task, then asks you for confirmation via elicitation. If you confirm, it "deletes" the file. If you decline, it cancels. Demonstrates `TaskElicit` — the task transitions to `input_required` while waiting for your response.

### 6. Sampling from a task (write_haiku)

```
Write a haiku about the ocean
```

The tool creates a task, then asks the LLM to write a haiku via sampling. The task transitions to `input_required` while the LLM generates, then returns the result. Demonstrates `TaskSample`.

### 7. Cancel a running task

```
Run a slow computation for 30 seconds, then cancel it
```

Start a long computation, then cancel before it finishes. Status transitions to `cancelled`.

### 8. List all tasks

```
List all tasks
```

Shows all tasks with their current status.

---

## Phase 2: TTL Enforcement ✅

> **Status**: Implemented. Tasks are automatically cleaned up after TTL expires.

### 9. Task expires after TTL

```bash
# Create a task with short TTL (5 seconds)
curl -s http://localhost:8080/mcp \
  -H "Mcp-Session-Id: <session-id>" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"slow_compute","arguments":{"seconds":2,"label":"short"},"task":{"ttl":5000}}}'
```

Wait 7 seconds, then poll:

```bash
curl -s http://localhost:8080/mcp \
  -H "Mcp-Session-Id: <session-id>" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":11,"method":"tasks/get","params":{"taskId":"<task-id>"}}'
```

**Expected:** Task not found — it was cleaned up after TTL expired.
**Behavior:** Timer resets on result storage and cancellation, so the TTL window starts fresh from the last state change.

---

## Phase 3: Session Isolation 🔲

> **Status**: Not yet implemented. These exercises will fail today — any session can access any task.

### 10. Cross-session task access denied

Create a task in session A:

```bash
curl -s -D- http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"session-a","version":"1.0"}}}'
# Note session A's Mcp-Session-Id
# Send notifications/initialized
# Create a task in session A
```

Initialize a second session B, then try to access session A's task:

```bash
# Initialize session B (separate curl, gets different session ID)
# Try tasks/get with session A's taskId from session B
```

**Expected (after Phase 3):** Task not found — session B can't see session A's tasks.
**Today:** Session B can access session A's tasks.

---

## Phase 4: Store API Alignment 🔲

> **Status**: Not yet implemented.

### 11. Double-complete is rejected

Complete a task, then try to store another result:

```bash
# Create and wait for task to complete
# Try to store a second result via internal API
```

**Expected (after Phase 4):** Error — can't store result for terminal task.
**Today:** No guard. Second result overwrites the first.

---

## Phase 5: Cancellation Propagation 🔲

> **Status**: Not yet implemented. Cancel marks the status but doesn't stop the goroutine.

### 12. Cancel actually stops the work

```bash
# Start a 60-second computation
curl -s http://localhost:8080/mcp \
  -H "Mcp-Session-Id: <session-id>" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":20,"method":"tools/call","params":{"name":"slow_compute","arguments":{"seconds":60,"label":"long"},"task":{}}}'

# Cancel it
curl -s http://localhost:8080/mcp \
  -H "Mcp-Session-Id: <session-id>" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":21,"method":"tasks/cancel","params":{"taskId":"<task-id>"}}'
```

**Expected (after Phase 5):** The server log shows the goroutine exited. Server resource usage drops.
**Today:** Status shows `cancelled` but the goroutine keeps sleeping for 60 seconds. Server log shows `[slow_compute] finished "long"` after 60s even though it was cancelled.

---

## Phase 6: Status Notifications 🔲

> **Status**: Not yet implemented. Clients must poll — no push notifications.

### 13. Receive status change notifications

Open a GET SSE stream, create a task, and watch for notifications:

```bash
# Terminal 1: open GET SSE stream
curl -N http://localhost:8080/mcp \
  -H "Mcp-Session-Id: <session-id>" \
  -H "Accept: text/event-stream"

# Terminal 2: create a task
# (same as exercise 2)
```

**Expected (after Phase 6):** The GET SSE stream receives `notifications/tasks/status` events as the task transitions: `working` → `completed`.
**Today:** No notifications. The SSE stream is silent. Clients must poll `tasks/get`.

---

## Phase 7: Progress Token Tracking 🔲

> **Status**: Not yet implemented.

### 14. Progress notifications flow through tasks

Create a task and send progress notifications from the tool handler:

**Expected (after Phase 7):** Progress notifications from the tool handler are associated with the task and delivered to the client.
**Today:** Progress token from the original `tools/call` is lost when the task is created.

---

## Phase 8: Sub-Task Threading 🔲

> **Status**: Not yet implemented. Tracked in #281.

### 15. Fan-out / join with sub-tasks

A "deploy" tool that fans out to parallel sub-tasks:

```
Deploy the app (build + test in parallel, then push)
```

**Expected (after Phase 8):**
- Parent task "deploy" created
- Child tasks "build" and "test" created with `parentTaskId` pointing to deploy
- Both children run in parallel
- Parent waits for both to complete (join)
- Parent continues with "push"
- `tasks/list` shows the full tree

**Today:** No sub-task support. The deploy tool would have to run everything sequentially in one goroutine.

### 16. Cascade cancel

```
Deploy the app, then cancel the deployment
```

**Expected (after Phase 8):** Cancelling the parent task cascades to all children — build and test are also cancelled.
**Today:** No cascade. Only the parent task is cancelled.

---

## Curl Walkthrough

For hosts that don't support tasks yet, you can exercise the async lifecycle with curl.

**Note:** `confirm_delete` and `write_haiku` cannot be fully tested with curl because they use the side-channel pattern — the server sends elicitation/sampling requests back to the client during `tasks/result`, which requires a client that can handle server-initiated requests. Use the Go test suite (`go test ./...`) to exercise these paths end-to-end.

### Initialize a session

```bash
curl -s -D- http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}'
```

Note the `Mcp-Session-Id` header — use it in all subsequent requests.

Send the initialized notification:

```bash
curl -s http://localhost:8080/mcp \
  -H "Mcp-Session-Id: <session-id>" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}'
```

### Start an async computation

```bash
curl -s http://localhost:8080/mcp \
  -H "Mcp-Session-Id: <session-id>" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"slow_compute","arguments":{"seconds":10,"label":"pi"},"task":{}}}'
```

Returns immediately with a `taskId`:

```json
{"result":{"task":{"taskId":"task-...","status":"working","ttl":300000,"pollInterval":1000}}}
```

### Poll task status

```bash
curl -s http://localhost:8080/mcp \
  -H "Mcp-Session-Id: <session-id>" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":3,"method":"tasks/get","params":{"taskId":"<task-id>"}}'
```

Returns `"status":"working"` while running, `"status":"completed"` when done.

### Get the result (blocks until done)

```bash
curl -s http://localhost:8080/mcp \
  -H "Mcp-Session-Id: <session-id>" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":4,"method":"tasks/result","params":{"taskId":"<task-id>"}}'
```

### Cancel a running task

```bash
curl -s http://localhost:8080/mcp \
  -H "Mcp-Session-Id: <session-id>" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":5,"method":"tasks/cancel","params":{"taskId":"<task-id>"}}'
```

### List all tasks

```bash
curl -s http://localhost:8080/mcp \
  -H "Mcp-Session-Id: <session-id>" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":6,"method":"tasks/list","params":{}}'
```

## Screenshots

### Async tool returns a task ID immediately

![Task Created](screenshots/task-created.png)

### Polling tasks/get — status transitions to completed

![Task Completed](screenshots/task-completed.png)

### failing_job — task transitions to failed after 1 second

![Task Failed](screenshots/task-failed.png)

## Key Files

| File | What |
|------|------|
| `main.go` | Server setup, 5 tools (greet, slow_compute, failing_job, confirm_delete, write_haiku), tasks registration |
| `test-side-by-side.sh` | Wire format comparison against TS SDK |
