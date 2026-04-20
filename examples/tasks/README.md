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

- **Without task support**: `slow_compute` blocks for the full duration (may timeout). `failing_job` returns an error saying it requires task invocation.
- **With task support**: The host sends a `"task": {}` hint, gets a task ID back immediately, and can poll/cancel/list tasks.

Until your host supports tasks, use the [curl walkthrough](#curl-walkthrough) below to exercise the async path.

## Exercises (task-capable hosts)

These prompts work if your host supports the MCP tasks protocol.

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

## Curl Walkthrough

For hosts that don't support tasks yet, you can exercise the async lifecycle with curl.

**Note:** `confirm_delete` and `write_haiku` cannot be fully tested with curl because they use the side-channel pattern — the server sends elicitation/sampling requests back to the client during `tasks/result`, which requires a client that can handle server-initiated requests. Use the Go test suite (`go test ./...`) to exercise these paths end-to-end.

### Initialize a session

```bash
curl -s -D- http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}'
```

Note the `Mcp-Session-Id` header — use it in all subsequent requests.

### Start an async computation

```bash
curl -s http://localhost:8080/mcp \
  -H "Mcp-Session-Id: <session-id>" \
  -H "Content-Type: application/json" \
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
  -d '{"jsonrpc":"2.0","id":3,"method":"tasks/get","params":{"taskId":"<task-id>"}}'
```

Returns `"status":"working"` while running, `"status":"completed"` when done.

### Get the result (blocks until done)

```bash
curl -s http://localhost:8080/mcp \
  -H "Mcp-Session-Id: <session-id>" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":4,"method":"tasks/result","params":{"taskId":"<task-id>"}}'
```

### Cancel a running task

```bash
curl -s http://localhost:8080/mcp \
  -H "Mcp-Session-Id: <session-id>" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":5,"method":"tasks/cancel","params":{"taskId":"<task-id>"}}'
```

### List all tasks

```bash
curl -s http://localhost:8080/mcp \
  -H "Mcp-Session-Id: <session-id>" \
  -H "Content-Type: application/json" \
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
