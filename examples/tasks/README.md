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

### Go server (mcpkit)

```bash
cd examples/tasks
go run . -addr :8080
```

### TS SDK reference server (for comparison)

The [TypeScript SDK](https://github.com/modelcontextprotocol/typescript-sdk) ships an equivalent `simpleTaskInteractive` example with the same `confirm_delete` and `write_haiku` tools. Run it to compare wire format side-by-side:

```bash
cd ~/projects/typescript-sdk   # or wherever you cloned it
pnpm install && pnpm build:all # first time only
PORT=8080 node examples/server/dist/simpleTaskInteractive.mjs
```

All curl exercises below produce identical responses from both servers (except task IDs and timestamps). Use `test-side-by-side.sh` for automated comparison.

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

Until your host supports tasks, use the curl commands in each exercise below (requires [curl prerequisites](#curl-prerequisites) first).

---

## Curl Prerequisites

All curl commands below use `$SESSION_ID` and `$TASK_ID` env vars so you can copy-paste without manual replacement. Requires [`jq`](https://jqlang.github.io/jq/).

The server returns SSE (`event: message\ndata: {...}`), not raw JSON. The helper function below extracts the JSON from the `data:` lines:

```bash
# Helper: extract JSON from SSE data lines, pretty-print, and save to /tmp/mcp-body.json
mcp() {
  curl -s "$@" | grep '^data: ' | sed 's/^data: //' | tee /tmp/mcp-body.json | jq .
}
```

**Note:** `confirm_delete` and `write_haiku` cannot be fully tested with curl because they use the side-channel pattern — the server sends elicitation/sampling requests back to the client during `tasks/result`, which requires a client that can handle server-initiated requests. Use the Go test suite (`go test ./...`) for those.

Initialize a session before running any curl exercises:

```bash
# Step 1: Initialize — prints response and captures SESSION_ID
curl -s -D /tmp/mcp-headers.txt http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}' \
  | grep '^data: ' | sed 's/^data: //' | jq .

export SESSION_ID=$(grep -i mcp-session-id /tmp/mcp-headers.txt | awk '{print $2}' | tr -d '\r')
echo "SESSION_ID=$SESSION_ID"

# Step 2: Send initialized notification
curl -s http://localhost:8080/mcp \
  -H "Mcp-Session-Id: $SESSION_ID" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}'
```

---

## Phase 1: Core Task Lifecycle ✅

These exercises work today. Each shows the **prompt** (for MCP hosts) and the **curl** equivalent.

### 1. Sync tool call

| Prompt | Curl |
|--------|------|
| `Greet World` | See below |

```bash
mcp http://localhost:8080/mcp \
  -H "Mcp-Session-Id: $SESSION_ID" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"greet","arguments":{"name":"World"}}}'
```

Returns immediately: `Hello, World!`

### 2. Async computation

| Prompt | Curl |
|--------|------|
| `Run a slow computation for 5 seconds labeled "pi"` | See below |

```bash
mcp http://localhost:8080/mcp \
  -H "Mcp-Session-Id: $SESSION_ID" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"slow_compute","arguments":{"seconds":5,"label":"pi"},"task":{}}}'

export TASK_ID=$(jq -r '.result.task.taskId' /tmp/mcp-body.json)
echo "TASK_ID=$TASK_ID"
```

Returns a task ID immediately. The computation runs in the background.

### 3. Check on a running task

| Prompt | Curl |
|--------|------|
| `What's the status of my computation?` | See below |

```bash
mcp http://localhost:8080/mcp \
  -H "Mcp-Session-Id: $SESSION_ID" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":4,\"method\":\"tasks/get\",\"params\":{\"taskId\":\"$TASK_ID\"}}"
```

The host polls `tasks/get` — status transitions from `working` to `completed`.

### 4. Failing job

| Prompt | Curl |
|--------|------|
| `Run the failing job` | See below |

```bash
mcp http://localhost:8080/mcp \
  -H "Mcp-Session-Id: $SESSION_ID" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"failing_job","arguments":{},"task":{}}}'

export TASK_ID=$(jq -r '.result.task.taskId' /tmp/mcp-body.json)
echo "TASK_ID=$TASK_ID"

# Wait 2s, then check status
sleep 2
mcp http://localhost:8080/mcp \
  -H "Mcp-Session-Id: $SESSION_ID" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":6,\"method\":\"tasks/get\",\"params\":{\"taskId\":\"$TASK_ID\"}}"
```

This tool *requires* task invocation. The job starts, then fails after 1 second — status transitions to `failed`.

### 5. Elicitation from a task (confirm_delete)

| Prompt | Curl |
|--------|------|
| `Delete the file important.txt` | See below (partial — curl can't respond to elicitation) |

```bash
# Create the task
mcp http://localhost:8080/mcp \
  -H "Mcp-Session-Id: $SESSION_ID" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"confirm_delete","arguments":{"filename":"important.txt"},"task":{}}}'

export TASK_ID=$(jq -r '.result.task.taskId' /tmp/mcp-body.json)
echo "TASK_ID=$TASK_ID"

# Poll status — should show "input_required" (task is waiting for your confirmation)
sleep 1
mcp http://localhost:8080/mcp \
  -H "Mcp-Session-Id: $SESSION_ID" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":11,\"method\":\"tasks/get\",\"params\":{\"taskId\":\"$TASK_ID\"}}"
```

The task transitions to `input_required` while waiting for the elicitation response. With curl, the task stays stuck here — the server sent an elicitation request via SSE but curl can't respond to it.

**To complete the flow**, use the Go test suite which has a mock elicitation handler:
```bash
cd experimental/ext/tasks && go test -run TestTaskElicitE2E -v
```

### 6. Sampling from a task (write_haiku)

| Prompt | Curl |
|--------|------|
| `Write a haiku about the ocean` | See below (partial — curl can't respond to sampling) |

```bash
# Create the task
mcp http://localhost:8080/mcp \
  -H "Mcp-Session-Id: $SESSION_ID" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":12,"method":"tools/call","params":{"name":"write_haiku","arguments":{"topic":"ocean"},"task":{}}}'

export TASK_ID=$(jq -r '.result.task.taskId' /tmp/mcp-body.json)
echo "TASK_ID=$TASK_ID"

# Poll status — should show "input_required" (task is waiting for LLM response)
sleep 1
mcp http://localhost:8080/mcp \
  -H "Mcp-Session-Id: $SESSION_ID" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":13,\"method\":\"tasks/get\",\"params\":{\"taskId\":\"$TASK_ID\"}}"
```

Same as confirm_delete — the task transitions to `input_required` while the server waits for a sampling response. Curl can't provide one.

**To complete the flow:**
```bash
cd experimental/ext/tasks && go test -run TestTaskSampleE2E -v
```

### 7. Cancel a running task

| Prompt | Curl |
|--------|------|
| `Run a slow computation for 30 seconds, then cancel it` | See below |

```bash
# Start a long computation
mcp http://localhost:8080/mcp \
  -H "Mcp-Session-Id: $SESSION_ID" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"slow_compute","arguments":{"seconds":30,"label":"long"},"task":{}}}'

export TASK_ID=$(jq -r '.result.task.taskId' /tmp/mcp-body.json)
echo "TASK_ID=$TASK_ID"

# Cancel it
mcp http://localhost:8080/mcp \
  -H "Mcp-Session-Id: $SESSION_ID" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":8,\"method\":\"tasks/cancel\",\"params\":{\"taskId\":\"$TASK_ID\"}}"
```

Start a long computation, then cancel before it finishes. Status transitions to `cancelled`.

### 8. List all tasks

| Prompt | Curl |
|--------|------|
| `List all tasks` | See below |

```bash
mcp http://localhost:8080/mcp \
  -H "Mcp-Session-Id: $SESSION_ID" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":9,"method":"tasks/list","params":{}}'
```

Shows all tasks with their current status.

---

## Phase 2: TTL Enforcement ✅

> **Status**: Implemented. Tasks are automatically cleaned up after TTL expires.

### 9. Task expires after TTL

| Prompt | Curl |
|--------|------|
| *(curl-only — needs custom TTL param)* | See below |

```bash
# Create a task with short TTL (5 seconds)
mcp http://localhost:8080/mcp \
  -H "Mcp-Session-Id: $SESSION_ID" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"slow_compute","arguments":{"seconds":2,"label":"short"},"task":{"ttl":5000}}}'

export TASK_ID=$(jq -r '.result.task.taskId' /tmp/mcp-body.json)
echo "TASK_ID=$TASK_ID"

# Wait for TTL to expire
sleep 7

# Poll — should be gone
mcp http://localhost:8080/mcp \
  -H "Mcp-Session-Id: $SESSION_ID" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":11,\"method\":\"tasks/get\",\"params\":{\"taskId\":\"$TASK_ID\"}}"
```

**Expected:** Task not found — it was cleaned up after TTL expired.
**Behavior:** Timer resets on result storage and cancellation, so the TTL window starts fresh from the last state change.

---

## Phase 3: Session Isolation 🔲

> **Status**: Not yet implemented. These exercises will fail today — any session can access any task.

### 10. Cross-session task access denied

| Prompt | Curl |
|--------|------|
| *(curl-only — needs two sessions)* | See below |

```bash
# Initialize session A
curl -s -D /tmp/mcp-headers-a.txt http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"session-a","version":"1.0"}}}' \
  | grep '^data: ' | sed 's/^data: //' | jq .

export SESSION_A=$(grep -i mcp-session-id /tmp/mcp-headers-a.txt | awk '{print $2}' | tr -d '\r')
echo "SESSION_A=$SESSION_A"

curl -s http://localhost:8080/mcp \
  -H "Mcp-Session-Id: $SESSION_A" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}'

# Create a task in session A
mcp http://localhost:8080/mcp \
  -H "Mcp-Session-Id: $SESSION_A" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"slow_compute","arguments":{"seconds":30,"label":"secret"},"task":{}}}'

export TASK_ID=$(jq -r '.result.task.taskId' /tmp/mcp-body.json)
echo "TASK_ID=$TASK_ID"

# Initialize session B
curl -s -D /tmp/mcp-headers-b.txt http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"session-b","version":"1.0"}}}' \
  | grep '^data: ' | sed 's/^data: //' | jq .

export SESSION_B=$(grep -i mcp-session-id /tmp/mcp-headers-b.txt | awk '{print $2}' | tr -d '\r')
echo "SESSION_B=$SESSION_B"

curl -s http://localhost:8080/mcp \
  -H "Mcp-Session-Id: $SESSION_B" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}'

# Try to access session A's task from session B
mcp http://localhost:8080/mcp \
  -H "Mcp-Session-Id: $SESSION_B" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"tasks/get\",\"params\":{\"taskId\":\"$TASK_ID\"}}"
```

**Expected (after Phase 3):** Task not found — session B can't see session A's tasks.
**Today:** Session B can access session A's tasks.

---

## Phase 4: Store API Alignment 🔲

> **Status**: Not yet implemented.

### 11. Double-complete is rejected

Complete a task, then try to store another result (internal API — no curl equivalent).

**Expected (after Phase 4):** Error — can't store result for terminal task.
**Today:** No guard. Second result overwrites the first.

---

## Phase 5: Cancellation Propagation 🔲

> **Status**: Not yet implemented. Cancel marks the status but doesn't stop the goroutine.

### 12. Cancel actually stops the work

| Prompt | Curl |
|--------|------|
| `Run a slow computation for 60 seconds, then cancel it` | See below |

```bash
# Start a 60-second computation
mcp http://localhost:8080/mcp \
  -H "Mcp-Session-Id: $SESSION_ID" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":20,"method":"tools/call","params":{"name":"slow_compute","arguments":{"seconds":60,"label":"long"},"task":{}}}'

export TASK_ID=$(jq -r '.result.task.taskId' /tmp/mcp-body.json)
echo "TASK_ID=$TASK_ID"

# Cancel it
mcp http://localhost:8080/mcp \
  -H "Mcp-Session-Id: $SESSION_ID" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":21,\"method\":\"tasks/cancel\",\"params\":{\"taskId\":\"$TASK_ID\"}}"
```

**Expected (after Phase 5):** The server log shows the goroutine exited. Server resource usage drops.
**Today:** Status shows `cancelled` but the goroutine keeps sleeping for 60 seconds. Server log shows `[slow_compute] finished "long"` after 60s even though it was cancelled.

---

## Phase 6: Status Notifications 🔲

> **Status**: Not yet implemented. Clients must poll — no push notifications.

### 13. Receive status change notifications

| Prompt | Curl |
|--------|------|
| *(host auto-receives notifications)* | See below |

```bash
# Terminal 1: open GET SSE stream
curl -N http://localhost:8080/mcp \
  -H "Mcp-Session-Id: $SESSION_ID" \
  -H "Accept: text/event-stream"

# Terminal 2: create a task (same as exercise 2)
```

**Expected (after Phase 6):** The GET SSE stream receives `notifications/tasks/status` events as the task transitions: `working` → `completed`.
**Today:** No notifications. The SSE stream is silent. Clients must poll `tasks/get`.

---

## Phase 7: Progress Token Tracking 🔲

> **Status**: Not yet implemented.

### 14. Progress notifications flow through tasks

Create a task and send progress notifications from the tool handler.

**Expected (after Phase 7):** Progress notifications from the tool handler are associated with the task and delivered to the client.
**Today:** Progress token from the original `tools/call` is lost when the task is created.

---

## Phase 8: Sub-Task Threading 🔲

> **Status**: Not yet implemented. Tracked in #281.

### 15. Fan-out / join with sub-tasks

| Prompt | Curl |
|--------|------|
| `Deploy the app (build + test in parallel, then push)` | *(not yet implemented)* |

**Expected (after Phase 8):**
- Parent task "deploy" created
- Child tasks "build" and "test" created with `parentTaskId` pointing to deploy
- Both children run in parallel
- Parent waits for both to complete (join)
- Parent continues with "push"
- `tasks/list` shows the full tree

**Today:** No sub-task support. The deploy tool would have to run everything sequentially in one goroutine.

### 16. Cascade cancel

| Prompt | Curl |
|--------|------|
| `Deploy the app, then cancel the deployment` | *(not yet implemented)* |

**Expected (after Phase 8):** Cancelling the parent task cascades to all children — build and test are also cancelled.
**Today:** No cascade. Only the parent task is cancelled.

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
