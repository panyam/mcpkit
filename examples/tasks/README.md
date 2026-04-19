# Tasks Example Server

Demonstrates MCP Tasks (spec 2025-11-25) — async tool execution with lifecycle tracking.

## MCPKit Features Used

| Category | Feature |
|----------|---------|
| Core | `core.ToolDef.Execution`, `core.TaskSupportOptional`, `core.TaskSupportRequired` |
| Experimental | `experimental/ext/tasks` — `tasks.Register`, `tasks.Config` |
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

## Exercises

### 1. Sync tool call

```
Greet World
```

Returns immediately: `Hello, World!`

### 2. Async computation (optional task support)

```
Run a slow computation for 5 seconds labeled "pi"
```

If the host supports tasks, this returns a task ID immediately and the computation runs in the background. Poll for the result — after 5 seconds it completes with `Result: 42`.

Without task support, the call blocks for 5 seconds and returns the result directly.

### 3. Check on a running task

```
What's the status of my computation?
```

The host polls `tasks/get` — status transitions from `working` to `completed`.

### 4. Failing job (required task support)

```
Run the failing job
```

This tool *requires* task invocation. The host must send a task hint. The job starts, then fails after 1 second — status transitions to `failed`.

### 5. Cancel a running task

```
Run a slow computation for 30 seconds, then cancel it
```

Start a long computation, then cancel before it finishes. Status transitions to `cancelled`.

### 6. List all tasks

```
List all tasks
```

Shows all tasks with their current status.

## Wire-Level Reference

For hosts that don't support tasks natively, or for manual testing with curl, here are the raw JSON-RPC payloads.

<details>
<summary>Sync tool call</summary>

```json
{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"greet","arguments":{"name":"World"}}}
```
</details>

<details>
<summary>Async tool call with task hint</summary>

```json
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"slow_compute","arguments":{"seconds":5,"label":"pi"},"task":{}}}
```

Returns `CreateTaskResult` with a `taskId`.
</details>

<details>
<summary>Poll task status</summary>

```json
{"jsonrpc":"2.0","id":3,"method":"tasks/get","params":{"taskId":"task-..."}}
```
</details>

<details>
<summary>Get task result (blocks until done)</summary>

```json
{"jsonrpc":"2.0","id":4,"method":"tasks/result","params":{"taskId":"task-..."}}
```
</details>

<details>
<summary>Cancel a running task</summary>

```json
{"jsonrpc":"2.0","id":5,"method":"tasks/cancel","params":{"taskId":"task-..."}}
```
</details>

<details>
<summary>List all tasks</summary>

```json
{"jsonrpc":"2.0","id":6,"method":"tasks/list","params":{}}
```
</details>

<details>
<summary>Forbidden: greet with task hint (error)</summary>

```json
{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"greet","arguments":{"name":"World"},"task":{}}}
```

Returns error: tool does not support task invocation.
</details>

<details>
<summary>Required: failing_job without task hint (error)</summary>

```json
{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"failing_job","arguments":{}}}
```

Returns error: tool requires task invocation.
</details>

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
| `main.go` | Server setup, 3 tools, tasks registration |
