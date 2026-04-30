# MCP Tasks v2 (SEP-2663) — Server-Directed Async + MRTR

Walks through the v2 Tasks extension where the *server* decides whether to create a task — clients no longer send a task hint. Polymorphic tools/call, inlined results, ack-only cancel, and the new tasks/update flow that closes the elicit/sample (MRTR) loop.

## What you'll learn

- **Connect to the v2 tasks server (declare extension)** — `client.WithTasksExtension()` adds `io.modelcontextprotocol/tasks` to ClientCapabilities.Extensions during initialize. Without that declaration, the v2 server falls through to synchronous tools/call and rejects tasks/* with -32601.
- **Sync call: greet — ToolCall returns Sync variant** — `client.ToolCall(c, name, args)` returns a polymorphic `*ToolCallResult`. For sync tools (no Execution / TaskSupport=forbidden) the server returns a plain `ToolResult` and the helper sets `Sync` (not `Task`). Callers branch on `result.IsTask()`.
- **slow_compute (no task hint!) — server creates a task → ToolCall returns Task variant** — Critical v2 semantics: no `task` param in the request — the server elects to create a task because slow_compute has TaskSupport=optional. The discriminator `result_type: "task"` lights up `result.IsTask()` on the helper. The Mcp-Name HTTP header carries the same taskId so HTTP routing/observability can key off it without parsing the body.
- **failing_job → status: completed, result.isError: true (TOOL error semantics)** — In v2, a tool that returns an error result lands in status `completed` with `result.isError: true`. The task itself ran to completion — the *operation* failed but the *infrastructure* didn't. Distinct from protocol failures (next step).
- **protocol_error_job → status: failed, error: {...} (PROTOCOL error semantics)** — Protocol errors (panics, framework bugs, things that aren't the tool's fault) land in status `failed` with the error inlined as `error: {code, message, data}` mirroring the JSON-RPC error shape. The host should treat this as 'something is broken', not 'the tool said no'.
- **confirm_delete → input_required → tasks/update → completed (SEP-2663 MRTR)** — This is the new SEP-2663 MRTR loop: the tool blocks on `TaskElicit`, the task parks in `input_required`, `tasks/get` surfaces the pending request under `inputRequests` (server-minted opaque keys), and `client.UpdateTask` delivers the matching response so the goroutine resumes. Cancellation during input_required propagates via ctx.Done() — see `TestV2_ElicitCancelUnblocks` in server tests.
- **Cancel a long-running task → empty ack, status settles to cancelled** — Same cooperative cancellation as v1 (server cancels the goroutine context; tools that select on ctx.Done() exit cleanly), but the response shape changed: SEP-2663 cancel returns an empty `{}` ack. Observe the `cancelled` status via the next `tasks/get` (or `WaitForTask` which does it for you).

## Flow

```mermaid
sequenceDiagram
    participant Host as MCP Host (this client)
    participant Server as MCP Server (make serve)

    Note over Host,Server: Step 1: Connect to the v2 tasks server (declare extension)
    Host->>Server: POST /mcp — initialize (declares io.modelcontextprotocol/tasks)
    Server-->>Host: serverInfo + tasks extension advertised under capabilities.extensions

    Note over Host,Server: Step 2: Sync call: greet — ToolCall returns Sync variant
    Host->>Server: tools/call: greet {name: "world"}
    Server-->>Host: ToolResult (no result_type discriminator → ToolCallResult.Sync)

    Note over Host,Server: Step 3: slow_compute (no task hint!) — server creates a task → ToolCall returns Task variant
    Host->>Server: tools/call: slow_compute {seconds: 3}
    Server-->>Host: {result_type: "task", task: {taskId, status: working, ttlSeconds, ...}}
+ Mcp-Name: <taskId> response header (SEP-2243)

    Note over Host,Server: Step 4: failing_job → status: completed, result.isError: true (TOOL error semantics)
    Host->>Server: tools/call: failing_job → CreateTaskResult
    Host->>Server: WaitForTask polls tasks/get until terminal
    Server-->>Host: {status: completed, result: {isError: true, content: [...]}}

    Note over Host,Server: Step 5: protocol_error_job → status: failed, error: {...} (PROTOCOL error semantics)
    Host->>Server: tools/call: protocol_error_job → CreateTaskResult
    Host->>Server: WaitForTask polls tasks/get until terminal
    Server-->>Host: {status: failed, error: {code, message}}

    Note over Host,Server: Step 6: confirm_delete → input_required → tasks/update → completed (SEP-2663 MRTR)
    Host->>Server: tools/call: confirm_delete {filename: "important.txt"}
    Server-->>Host: {result_type: task, task: {status: working, ...}}
    Host->>Server: GetTask (polled until status = input_required)
    Server-->>Host: DetailedTask {status: input_required, inputRequests: { "elicit-N": {method, params} }}
    Host->>Server: tasks/update {taskId, inputResponses: { "elicit-N": {action: accept, content: {confirm: true}} }}
    Server-->>Host: {} (empty ack)
    Host->>Server: WaitForTask until terminal
    Server-->>Host: {status: completed, result: {content: ["deleted 'important.txt'"]}}

    Note over Host,Server: Step 7: Cancel a long-running task → empty ack, status settles to cancelled
    Host->>Server: tools/call: slow_compute {seconds: 10}
    Server-->>Host: {result_type: task, task: ...}
    Host->>Server: client.CancelTask
    Server-->>Host: {} (empty ack — SEP-2663 cancel returns no task state)
    Host->>Server: WaitForTask polls tasks/get
    Server-->>Host: {status: cancelled}
```

## Steps

### Setup

Start the MCP server in a separate terminal first:

```
Terminal 1:  make serve        # tasks-v2 server on :8080
Terminal 2:  make demo         # this demo
```

### v1 vs v2 — what changed

v1 (SEP-1036, MCP spec 2025-11-25) had the *client* hint at task vs sync via a `task` param. v2 (SEP-2663, in-flight) flips the contract:

- **Tasks is an extension** (`io.modelcontextprotocol/tasks`). Clients declare support during `initialize`; servers gate every task-creating `tools/call` and every `tasks/*` method on the negotiation.
- **No client task hint.** Just call `tools/call` normally — the *server* decides whether to run sync or create a task. The client `client.ToolCall` helper returns a polymorphic `ToolCallResult` with either `Sync` or `Task` populated.
- **`result_type` discriminator** on `tools/call` response: `"task"` means a task was created; absent means sync.
- **`tasks/get` returns `DetailedTask`** with inlined `result` / `error` / `inputRequests` / `requestState` per status. No separate `tasks/result` round-trip.
- **`tasks/cancel` returns an empty ack**. Observe the resulting `cancelled` status with the next `tasks/get`.
- **`tasks/update` is the SEP-2663 resume path** for MRTR input rounds — the client delivers `inputResponses` keyed to whatever `inputRequests` the server emitted.
- **Wire fields renamed**: `ttlSeconds`, `pollIntervalMilliseconds`. `parentTaskId` removed.
- **Mcp-Name HTTP header** (SEP-2243) carries the new taskId on task-creating responses.
- **Error semantics**: tool errors → `status: completed, isError: true`. Protocol errors → `status: failed` + `error` object.
- **`tasks/result` and `tasks/list` removed** — `tasks/get` is the single read endpoint.

### Step 1: Connect to the v2 tasks server (declare extension)

`client.WithTasksExtension()` adds `io.modelcontextprotocol/tasks` to ClientCapabilities.Extensions during initialize. Without that declaration, the v2 server falls through to synchronous tools/call and rejects tasks/* with -32601.

### Step 2: Sync call: greet — ToolCall returns Sync variant

`client.ToolCall(c, name, args)` returns a polymorphic `*ToolCallResult`. For sync tools (no Execution / TaskSupport=forbidden) the server returns a plain `ToolResult` and the helper sets `Sync` (not `Task`). Callers branch on `result.IsTask()`.

### Step 3: slow_compute (no task hint!) — server creates a task → ToolCall returns Task variant

Critical v2 semantics: no `task` param in the request — the server elects to create a task because slow_compute has TaskSupport=optional. The discriminator `result_type: "task"` lights up `result.IsTask()` on the helper. The Mcp-Name HTTP header carries the same taskId so HTTP routing/observability can key off it without parsing the body.

### Step 4: failing_job → status: completed, result.isError: true (TOOL error semantics)

In v2, a tool that returns an error result lands in status `completed` with `result.isError: true`. The task itself ran to completion — the *operation* failed but the *infrastructure* didn't. Distinct from protocol failures (next step).

### Step 5: protocol_error_job → status: failed, error: {...} (PROTOCOL error semantics)

Protocol errors (panics, framework bugs, things that aren't the tool's fault) land in status `failed` with the error inlined as `error: {code, message, data}` mirroring the JSON-RPC error shape. The host should treat this as 'something is broken', not 'the tool said no'.

### Step 6: confirm_delete → input_required → tasks/update → completed (SEP-2663 MRTR)

This is the new SEP-2663 MRTR loop: the tool blocks on `TaskElicit`, the task parks in `input_required`, `tasks/get` surfaces the pending request under `inputRequests` (server-minted opaque keys), and `client.UpdateTask` delivers the matching response so the goroutine resumes. Cancellation during input_required propagates via ctx.Done() — see `TestV2_ElicitCancelUnblocks` in server tests.

### Step 7: Cancel a long-running task → empty ack, status settles to cancelled

Same cooperative cancellation as v1 (server cancels the goroutine context; tools that select on ctx.Done() exit cleanly), but the response shape changed: SEP-2663 cancel returns an empty `{}` ack. Observe the `cancelled` status via the next `tasks/get` (or `WaitForTask` which does it for you).

### Where each piece lives in mcpkit

- v2 server library: `server/tasks_v2.go` (RegisterTasks, gating, MRTR runtime)
- v2 wire types (`CreateTaskResult`, `DetailedTask`, `TaskInfoV2`, `UpdateTaskRequest`, `ResultTypeTask`): `core/task_v2.go` (SEP-2663)
- v2 client helpers (`ToolCall`, `GetTask`, `UpdateTask`, `WaitForTask`, `CancelTask`): `client/tasks.go`
- Conformance tests: `conformance/tasks-v2/scenarios.test.ts`
- Implementation plan + open questions: `PLAN.md`

## Run it

```bash
go run ./examples/tasks-v2/
```

Pass `--non-interactive` to skip pauses:

```bash
go run ./examples/tasks-v2/ --non-interactive
```
