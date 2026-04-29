# MCP Tasks — Async Tool Execution Lifecycle

Walks through the MCP Tasks (SEP-1036) lifecycle: optional/required task support, polling, progress notifications, and cancellation.

## What you'll learn

- **Connect to the MCP server** — The server advertises tasks capability in initialize. The mcpkit client opens a GET SSE stream so server-pushed notifications (progress, status changes) reach us during polling.
- **Sync call: greet (taskSupport=forbidden)** — greet is sync-only. The result returns directly in the tools/call response — no task created. This is the path most existing tools use today; tasks are opt-in per tool.
- **Optional task: slow_compute as task → CreateTaskResult** — slow_compute has taskSupport=optional. Sending the `task` hint tells the server to run it asynchronously. We get a taskId back immediately while the work runs in a background goroutine.
- **Poll tasks/get until terminal; receive notifications/progress** — The server streams progress notifications over the GET SSE channel while the task runs. Our notification callback (set up in Step 1) prints them inline. Once status reaches `completed`, the polling stops.
- **Fetch the result payload via tasks/result** — tasks/get returns task status only. To get the actual tool result (content blocks, isError flag, structured content), the host calls tasks/result with the same taskId.
- **Required task: failing_job — sync call returns an error** — failing_job declares Execution.TaskSupport=required. Sync invocation returns an error telling the host to retry with a task hint. This guards expensive/long tools from blocking the request thread.
- **Invoke failing_job as task → terminal status: failed** — Errors from required-task tools surface as a terminal status of `failed`. The host gets the taskId immediately, polls, and learns the task failed via the status field — no exception thrown on the polling call.
- **Cancel a long-running task mid-flight** — Tasks support cooperative cancellation. The server cancels the goroutine's context; tools that select on ctx.Done() exit cleanly. Status transitions to `cancelled`. mcpkit guards against terminal-to-terminal transitions so a tool finishing normally after cancel doesn't overwrite the cancelled status.

## Flow

```mermaid
sequenceDiagram
    participant Host as MCP Host (this client)
    participant Server as MCP Server (make serve)

    Note over Host,Server: Step 1: Connect to the MCP server
    Host->>Server: POST /mcp — initialize
    Server-->>Host: serverInfo + Mcp-Session-Id + tasks capability

    Note over Host,Server: Step 2: Sync call: greet (taskSupport=forbidden)
    Host->>Server: tools/call: greet {name: "world"}  (no task hint)
    Server-->>Host: ToolResult immediately

    Note over Host,Server: Step 3: Optional task: slow_compute as task → CreateTaskResult
    Host->>Server: tools/call: slow_compute {seconds:3} + task: {ttl: 60s}
    Server-->>Host: {taskId, status: working, ttl, pollInterval}

    Note over Host,Server: Step 4: Poll tasks/get until terminal; receive notifications/progress
    Host->>Server: tasks/get {taskId}  (polled every pollInterval)
    Server-->>Host: notifications/progress (1/3, 2/3, 3/3) via SSE
    Server-->>Host: {status: completed} on terminal poll

    Note over Host,Server: Step 5: Fetch the result payload via tasks/result
    Host->>Server: tasks/result {taskId}
    Server-->>Host: ToolResult

    Note over Host,Server: Step 6: Required task: failing_job — sync call returns an error
    Host->>Server: tools/call: failing_job  (no task hint)
    Server-->>Host: JSON-RPC error (taskSupport=required)

    Note over Host,Server: Step 7: Invoke failing_job as task → terminal status: failed
    Host->>Server: tools/call: failing_job + task hint
    Server-->>Host: {taskId, status: working}
    Host->>Server: tasks/get (polled)
    Server-->>Host: {status: failed, error: "simulated failure"}

    Note over Host,Server: Step 8: Cancel a long-running task mid-flight
    Host->>Server: tools/call: slow_compute {seconds: 10} + task hint
    Server-->>Host: {taskId, status: working}
    Host->>Server: tasks/cancel {taskId}
    Server-->>Host: ack
    Host->>Server: tasks/get (final)
    Server-->>Host: {status: cancelled}
```

## Steps

### Setup

Start the MCP server in a separate terminal first:

```
Terminal 1:  make serve        # tasks server on :8080
Terminal 2:  make run          # this demo
```

### How tasks work

Each tool's `Execution.TaskSupport` declares whether it can run as a task:

- **forbidden** (or absent): sync-only. Calling with a `task` hint returns an error.
- **optional**: client chooses. `tools/call` blocks for the result; `tools/call` with `task` hint returns a `CreateTaskResult` immediately.
- **required**: must be invoked as a task. Direct sync calls return an error.

For task invocations, the server responds with `{taskId, ttl, pollInterval}`. The host polls `tasks/get` until the status is terminal (`completed`, `failed`, or `cancelled`), then fetches the result via `tasks/result`. Progress notifications stream over the session's GET SSE channel.

### Step 1: Connect to the MCP server

The server advertises tasks capability in initialize. The mcpkit client opens a GET SSE stream so server-pushed notifications (progress, status changes) reach us during polling.

### Step 2: Sync call: greet (taskSupport=forbidden)

greet is sync-only. The result returns directly in the tools/call response — no task created. This is the path most existing tools use today; tasks are opt-in per tool.

### Step 3: Optional task: slow_compute as task → CreateTaskResult

slow_compute has taskSupport=optional. Sending the `task` hint tells the server to run it asynchronously. We get a taskId back immediately while the work runs in a background goroutine.

### Step 4: Poll tasks/get until terminal; receive notifications/progress

The server streams progress notifications over the GET SSE channel while the task runs. Our notification callback (set up in Step 1) prints them inline. Once status reaches `completed`, the polling stops.

### Step 5: Fetch the result payload via tasks/result

tasks/get returns task status only. To get the actual tool result (content blocks, isError flag, structured content), the host calls tasks/result with the same taskId.

### Step 6: Required task: failing_job — sync call returns an error

failing_job declares Execution.TaskSupport=required. Sync invocation returns an error telling the host to retry with a task hint. This guards expensive/long tools from blocking the request thread.

### Step 7: Invoke failing_job as task → terminal status: failed

Errors from required-task tools surface as a terminal status of `failed`. The host gets the taskId immediately, polls, and learns the task failed via the status field — no exception thrown on the polling call.

### Step 8: Cancel a long-running task mid-flight

Tasks support cooperative cancellation. The server cancels the goroutine's context; tools that select on ctx.Done() exit cleanly. Status transitions to `cancelled`. mcpkit guards against terminal-to-terminal transitions so a tool finishing normally after cancel doesn't overwrite the cancelled status.

### Where each piece lives in mcpkit

- Tasks server library: `server/task_*.go`, `server/tasks_experimental.go`
- TaskContext (used by required-task tools): `core.TaskContext` — `core/task.go`
- Client helpers: `client/tasks.go` — `ToolCallAsTask`, `WaitForTask`, `GetTask`, `GetTaskPayload`, `CancelTask`
- Tool declares task support via `core.ToolDef.Execution.TaskSupport` (`forbidden` | `optional` | `required`)

For elicitation/sampling from inside a task (the `confirm_delete` and `write_haiku` tools also registered on this server), see `examples/tasks/run-exercises.sh`.

## Run it

```bash
go run ./examples/tasks/
```

Pass `--non-interactive` to skip pauses:

```bash
go run ./examples/tasks/ --non-interactive
```
