# MCP Tasks — Async Tool Execution

EXPERIMENTAL library implementing the MCP Tasks protocol (spec 2025-11-25). Enables "call-now, fetch-later" async tool execution on mcpkit servers.

Spec reference: https://modelcontextprotocol.io/specification/2025-11-25/basic/utilities/tasks

## Quick Start

```go
srv := server.NewServer(core.ServerInfo{Name: "my-server", Version: "1.0"})

// Register a tool with optional task support.
srv.RegisterTool(
    core.ToolDef{
        Name:      "slow_job",
        Execution: &core.ToolExecution{TaskSupport: core.TaskSupportOptional},
        // ...
    },
    handler,
)

// Enable tasks on the server. Must be called before accepting connections.
tasks.Register(tasks.Config{Server: srv})
```

## Task Lifecycle

```
                 ┌─────────────┐
                 │   working    │
                 └──────┬───────┘
                        │
            ┌───────────┼───────────┐
            ▼           ▼           ▼
     ┌────────────┐ ┌────────┐ ┌───────────┐
     │ completed  │ │ failed │ │ cancelled │
     └────────────┘ └────────┘ └───────────┘
```

`input_required` (paused for elicitation) is a defined status but not yet implemented.

## Wire Format

| Method | Request | Response |
|--------|---------|----------|
| `tools/call` + `"task": {}` | `{"name": "...", "arguments": {...}, "task": {"ttl": 60000}}` | `{"task": {"taskId": "...", "status": "working", ...}}` |
| `tasks/get` | `{"taskId": "..."}` | Flat: `{"taskId": "...", "status": "...", "ttl": 30000, ...}` |
| `tasks/result` | `{"taskId": "..."}` | Original ToolResult + `_meta["io.modelcontextprotocol/related-task"]` |
| `tasks/list` | `{"cursor": ""}` | `{"tasks": [...], "nextCursor": "..."}` |
| `tasks/cancel` | `{"taskId": "..."}` | Flat: `{"taskId": "...", "status": "cancelled", ...}` |

## TaskSupport Semantics

Set on `ToolDef.Execution.TaskSupport`:

| Value | Meaning |
|-------|---------|
| `"required"` | Client MUST include `task` hint. Server errors without it (`-32601`). |
| `"optional"` | Client may include or omit `task` hint. |
| `"forbidden"` | Client MUST NOT include `task` hint. Server errors with it (`-32601`). |
| *(absent)* | Same as `"forbidden"`. Tools without `Execution` field don't support tasks. |

## Error Codes

| Scenario | Code |
|----------|------|
| Required tool without hint | `-32601` |
| Forbidden/absent tool with hint | `-32601` |
| Nonexistent taskId | `-32602` |
| Cancel already-terminal task | `-32602` |
| Failed task via tasks/result | `-31000` |

## Server API

### `tasks.Register(cfg Config)`

Hooks up tasks support: middleware, method handlers, capability advertisement.

**Config fields:**
- `Server` — the MCP server (required)
- `Store` — TaskStore implementation (default: InMemoryTaskStore)
- `DefaultTTLMs` — default task TTL in ms (default: 300000)
- `DefaultPollMs` — suggested poll interval in ms (default: 1000)

### `TaskStore` interface

```go
type TaskStore interface {
    Create(info core.TaskInfo) error
    Get(taskID string) (core.TaskInfo, bool)
    Update(taskID string, fn func(*core.TaskInfo)) error
    SetResult(taskID string, result core.ToolResult) error
    GetResult(taskID string) (core.ToolResult, bool)
    WaitForResult(taskID string) (core.ToolResult, core.TaskInfo, error)
    List(cursor string, limit int) ([]core.TaskInfo, string)
    Cancel(taskID string) (core.TaskInfo, error)
}
```

`InMemoryTaskStore` is the default. The interface exists for multi-node scenarios (e.g., Redis-backed shared state).

## Client API

```go
// Create a task for a tool call.
created, err := tasks.ToolCallAsTask(c, "slow_job", args, 60000)

// Poll status (non-blocking).
got, err := tasks.GetTask(c, created.Task.TaskID)

// Get result (blocks until terminal).
result, taskID, err := tasks.GetTaskPayload(c, created.Task.TaskID)

// List all tasks.
list, err := tasks.ListTasks(c, "")

// Cancel a running task.
cancelled, err := tasks.CancelTask(c, created.Task.TaskID)
```

## Not Yet Implemented

- `notifications/tasks/status` — optional status change notifications
- `input_required` flow — task pausing for elicitation/sampling
- Bidirectional tasks — server-to-client task augmentation
- TTL-based task cleanup
- Session-scoped task isolation
