# MCP Tasks v2 (SEP-2557) — Server-Directed Async

Walks through the v2 Tasks protocol where the *server* decides whether to create a task — clients no longer send a task hint. Inlined results, flat shape, and tool-error vs protocol-error semantics.

## What you'll learn

- **Connect to the v2 tasks server** — The mcpkit client opens a GET SSE stream so progress notifications reach us during polling. Initialize advertises the v2 tasks capability.
- **Sync call: greet — no task created** — greet is taskSupport=forbidden. The server runs it inline and returns the standard ToolResult shape. No task created. The host can detect sync vs task by checking for the `resultType: "task"` discriminator on the response.
- **slow_compute (no task hint!) — server creates a task** — Critical v2 semantics: client doesn't include a `task` param — calls slow_compute like any sync tool. Because slow_compute has Execution.TaskSupport=optional, the server elects to create a task. The discriminator `resultType: "task"` tells the host to switch to polling mode.
- **Poll tasks/get; final response inlines the result** — v2's flat shape: GetTaskResultV2 has TaskInfo fields at the top level, and inlines the actual ToolResult under `result` once status is terminal. No second roundtrip to tasks/result.
- **failing_job → status: completed, result.isError: true (TOOL error semantics)** — In v2, a tool that returns an error result lands in status `completed` with `result.isError: true`. The task itself ran to completion — the *operation* failed but the *infrastructure* didn't. This is distinct from protocol failures (next step).
- **protocol_error_job → status: failed, error: {...} (PROTOCOL error semantics)** — Protocol errors (panics, framework bugs, things that aren't the tool's fault) land in status `failed` with the error inlined as `error: {code, message, data}` mirroring JSON-RPC error shape. The host should treat this as 'something is broken', not 'the tool said no'.
- **Cancel a long-running task → status: cancelled** — Same cooperative cancellation as v1. Server cancels the goroutine context; tools that select on ctx.Done() exit cleanly. v2 cancel response also includes the flat TaskInfo so the host doesn't need an extra round-trip.

## Flow

```mermaid
sequenceDiagram
    participant Host as MCP Host (this client)
    participant Server as MCP Server (make serve)

    Note over Host,Server: Step 1: Connect to the v2 tasks server
    Host->>Server: POST /mcp — initialize
    Server-->>Host: serverInfo + tasks v2 capability

    Note over Host,Server: Step 2: Sync call: greet — no task created
    Host->>Server: tools/call: greet {name: "world"}
    Server-->>Host: ToolResult (no resultType discriminator → sync)

    Note over Host,Server: Step 3: slow_compute (no task hint!) — server creates a task
    Host->>Server: tools/call: slow_compute {seconds: 3}
    Server-->>Host: {resultType: "task", task: {taskId, status: working, ttl}}

    Note over Host,Server: Step 4: Poll tasks/get; final response inlines the result
    Host->>Server: tasks/get {taskId}  (polled)
    Server-->>Host: notifications/progress (1/3, 2/3, 3/3) via SSE
    Server-->>Host: {status: completed, result: {...}, ttl: ...}  (no separate tasks/result needed)

    Note over Host,Server: Step 5: failing_job → status: completed, result.isError: true (TOOL error semantics)
    Host->>Server: tools/call: failing_job
    Server-->>Host: {resultType: task, task: ...}
    Host->>Server: tasks/get (polled)
    Server-->>Host: {status: completed, result: {isError: true, content: [...]}}

    Note over Host,Server: Step 6: protocol_error_job → status: failed, error: {...} (PROTOCOL error semantics)
    Host->>Server: tools/call: protocol_error_job
    Host->>Server: tasks/get (polled)
    Server-->>Host: {status: failed, error: {code, message}}

    Note over Host,Server: Step 7: Cancel a long-running task → status: cancelled
    Host->>Server: tools/call: slow_compute {seconds: 10}
    Server-->>Host: {resultType: task, task: ...}
    Host->>Server: tasks/cancel {taskId}
    Host->>Server: tasks/get (final)
    Server-->>Host: {status: cancelled}
```

## Steps

### Setup

Start the MCP server in a separate terminal first:

```
Terminal 1:  make serve        # tasks-v2 server on :8080
Terminal 2:  make run          # this demo
```

### v1 vs v2 — what changed

In v1 (SEP-1036) the *client* hints at task vs sync via a `task` param. v2 (SEP-2557) flips this:

- **No client task hint.** Just call `tools/call` normally — the *server* decides.
- **`resultType` discriminator** on `tools/call` response: `"task"` means a task was created (poll); absent means sync result.
- **Inlined `result` / `error` / `inputRequests`** on `tasks/get` — no separate `tasks/result` call.
- **TTL in seconds** (was milliseconds in v1).
- **Error semantics**: tool errors (logic failures) → `status: completed, isError: true`. Protocol errors (framework crashes) → `status: failed` + `error` object.
- **`tasks/result` and `tasks/list` removed** — `tasks/get` is the single read endpoint.

### Step 1: Connect to the v2 tasks server

The mcpkit client opens a GET SSE stream so progress notifications reach us during polling. Initialize advertises the v2 tasks capability.

### Step 2: Sync call: greet — no task created

greet is taskSupport=forbidden. The server runs it inline and returns the standard ToolResult shape. No task created. The host can detect sync vs task by checking for the `resultType: "task"` discriminator on the response.

### Step 3: slow_compute (no task hint!) — server creates a task

Critical v2 semantics: client doesn't include a `task` param — calls slow_compute like any sync tool. Because slow_compute has Execution.TaskSupport=optional, the server elects to create a task. The discriminator `resultType: "task"` tells the host to switch to polling mode.

### Step 4: Poll tasks/get; final response inlines the result

v2's flat shape: GetTaskResultV2 has TaskInfo fields at the top level, and inlines the actual ToolResult under `result` once status is terminal. No second roundtrip to tasks/result.

### Step 5: failing_job → status: completed, result.isError: true (TOOL error semantics)

In v2, a tool that returns an error result lands in status `completed` with `result.isError: true`. The task itself ran to completion — the *operation* failed but the *infrastructure* didn't. This is distinct from protocol failures (next step).

### Step 6: protocol_error_job → status: failed, error: {...} (PROTOCOL error semantics)

Protocol errors (panics, framework bugs, things that aren't the tool's fault) land in status `failed` with the error inlined as `error: {code, message, data}` mirroring JSON-RPC error shape. The host should treat this as 'something is broken', not 'the tool said no'.

### Step 7: Cancel a long-running task → status: cancelled

Same cooperative cancellation as v1. Server cancels the goroutine context; tools that select on ctx.Done() exit cleanly. v2 cancel response also includes the flat TaskInfo so the host doesn't need an extra round-trip.

### Where each piece lives in mcpkit

- v2 server library: `server/tasks_v2.go`
- v2 wire types (`CreateTaskResultV2`, `GetTaskResultV2`, `ResultTypeTask`): `core/task_v2.go`
- Conformance tests: `conformance/tasks-v2/scenarios.test.ts` (21 scenarios)
- Implementation plan: `docs/TASKS_V2_PLAN.md`

## Run it

```bash
go run ./examples/tasks-v2/
```

Pass `--non-interactive` to skip pauses:

```bash
go run ./examples/tasks-v2/ --non-interactive
```
