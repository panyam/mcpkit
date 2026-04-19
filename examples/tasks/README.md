# Tasks Example Server

Demonstrates MCP Tasks (spec 2025-11-25) — async tool execution with lifecycle tracking.

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

## Step-by-Step Manual Testing

### 1. Sync tool call (greet)

Call `greet` with `{"name": "World"}`. Returns immediately: `Hello, World!`

### 2. Forbidden tool with task hint

Call `greet` with task hint:
```json
{"name": "greet", "arguments": {"name": "World"}, "task": {}}
```
Returns error: tool does not support task invocation.

### 3. Async tool call (slow_compute)

Call `slow_compute` with task hint:
```json
{"name": "slow_compute", "arguments": {"seconds": 5, "label": "pi"}, "task": {}}
```

Returns immediately with `CreateTaskResult`:
```json
{"task": {"taskId": "task-...", "status": "working", "ttl": 300000, "pollInterval": 1000, ...}}
```

### 4. Poll task status

```json
{"method": "tasks/get", "params": {"taskId": "task-..."}}
```

Returns flat task info: `{"taskId": "...", "status": "working", ...}`

### 5. Get task result

```json
{"method": "tasks/result", "params": {"taskId": "task-..."}}
```

Blocks until completion, then returns the original `ToolResult` with `_meta`:
```json
{"content": [{"type": "text", "text": "Computation \"pi\" completed after 5 seconds. Result: 42."}], "_meta": {"io.modelcontextprotocol/related-task": {"taskId": "task-..."}}}
```

### 6. Required task tool without hint

Call `failing_job` without task hint:
```json
{"name": "failing_job", "arguments": {}}
```
Returns error: tool requires task invocation.

### 7. Required task tool with hint

```json
{"name": "failing_job", "arguments": {}, "task": {}}
```

Returns `CreateTaskResult`. Poll with `tasks/get` — status transitions to `failed`.

### 8. Cancel a running task

Start `slow_compute` with a long duration, then:
```json
{"method": "tasks/cancel", "params": {"taskId": "task-..."}}
```

Returns flat: `{"taskId": "...", "status": "cancelled", ...}`

### 9. List all tasks

```json
{"method": "tasks/list", "params": {}}
```

Returns: `{"tasks": [...], "nextCursor": "..."}`

## Key Files

| File | What |
|------|------|
| `main.go` | Server setup, 3 tools, tasks registration |
