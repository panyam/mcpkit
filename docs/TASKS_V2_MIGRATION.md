# Tasks v1 → v2 Migration Guide

This guide explains how to move an mcpkit server or client from the v1 task surface (MCP spec 2025-11-25) to the v2 surface (SEP-2663 Tasks Extension), and how to keep both running on the same server during a rolling transition.

> **Status:** v2 is in spec draft (SEP-2663) at the time of writing. The wire shapes implemented here track the spec; expect minor follow-ups as it finalizes. v1 stays frozen and supported.

## TL;DR

| You are | You want | Do |
|---|---|---|
| Building a new server today | v2 only | `tasks.Register(tasks.Config{Server: srv})` (import `github.com/panyam/mcpkit/ext/tasks`) |
| Maintaining an existing v1 server | Keep working v1 clients alive | `server.RegisterTasksV1(server.TasksConfigV1{Server: srv})` (no change — `RegisterTasks` was renamed) |
| Migrating a v1 server to v2 | Both clients on one endpoint | Register both independently: `server.RegisterTasksV1(...)` + `tasks.Register(...)`. The previous `RegisterTasksHybrid` was removed when v2 moved to `ext/tasks/`. Last-write-wins on `tasks/get` / `tasks/cancel` registration. |
| Building a new v2 client | – | `client.WithTasksExtension()` + `client.ToolCall` / `GetTask` / `UpdateTask` / `WaitForTask` / `CancelTask` |
| Maintaining a v1 client | – | `client.ToolCallAsTaskV1` / `GetTaskV1` / etc. (renamed; behavior unchanged) |

## What changed

SEP-2663 evolves the v1 task surface in five ways. Each one has a wire-format diff and a code-shape diff.

### 1. Tasks is now an extension, not a top-level capability

| | v1 | v2 |
|---|---|---|
| `initialize` advertisement | `capabilities.tasks` | `capabilities.extensions["io.modelcontextprotocol/tasks"]` |
| Client opt-in | (none required for the basic flow) | `client.WithTasksExtension()` MUST be set |
| Per-request opt-in | n/a | SEP-2575 `_meta.io.modelcontextprotocol/clientCapabilities` |
| Server gating | none | `tasks/*` returns `-32601` if extension not negotiated; `tools/call` falls through to sync |

### 2. tools/call response is polymorphic (`resultType` discriminator)

v1 server decides based on whether the client sent a `task` hint. v2 server decides unilaterally — and the client doesn't send a hint. The response carries a `resultType` discriminator so the client knows which shape arrived.

```jsonc
// v1 — sync
{ "content": [...], "isError": false }
// v1 — task (when client sent params.task)
{ "task": { "taskId": "...", "ttl": 60000, "pollInterval": 1000, ... } }

// v2 — sync
{ "content": [...], "isError": false }
// v2 — task (server elected; SEP-2663 flat shape: Result & Task)
{ "resultType": "task", "taskId": "...", "status": "working", "ttlMs": 60000, "pollIntervalMs": 1000 }
```

Client-side, use `client.ToolCall` (returns a polymorphic `*ToolCallResult`):

```go
res, err := client.ToolCall(c, "slow_compute", args)
if err != nil { ... }
if res.IsTask() {
    // poll res.Task.TaskID via client.WaitForTask / GetTask
} else {
    // res.Sync is a regular *core.ToolResult
}
```

### 3. tasks/get returns DetailedTask with inlined result/error/inputRequests

v1 needed two round-trips for a completed task: `tasks/get` for status, then `tasks/result` for the payload. v2 inlines everything on `tasks/get`:

```jsonc
// v2 tasks/get response (status: completed)
{
  "taskId": "task-abc",
  "status": "completed",
  "createdAt": "...",
  "lastUpdatedAt": "...",
  "ttlMs": 60000,
  "pollIntervalMs": 1000,
  "result": { "content": [...], "isError": false },
  "requestState": "opaque-token"
}
```

`tasks/result` and `tasks/list` are removed in v2.

### 4. tasks/update is the new MRTR resume path

When a v2 task transitions to `input_required`, `tasks/get` surfaces a map of pending input requests:

```jsonc
{ "status": "input_required", "inputRequests": { "elicit-1": { "method": "elicitation/create", "params": {...} } } }
```

The client replies via `tasks/update` (returns an empty `{}` ack):

```go
err := client.UpdateTask(c, core.UpdateTaskRequest{
    TaskID: taskID,
    InputResponses: core.InputResponses{
        "elicit-1": json.RawMessage(`{"action":"accept","content":{"confirm":true}}`),
    },
    RequestState: pending.RequestState, // SEP-2322 echo
})
```

Map keys are server-minted opaque strings. Clients MUST treat them as round-trip echo values.

### 5. tasks/cancel is ack-only; wire fields renamed

| | v1 | v2 |
|---|---|---|
| `tasks/cancel` response | `{taskId, status: cancelled, ...}` | `{}` (empty ack) |
| TTL field | `ttl` (ms by convention) | `ttlMs` (integer milliseconds, units in the name) |
| Poll-interval field | `pollInterval` | `pollIntervalMs` (integer milliseconds) |
| `parentTaskId` | present | removed |
| Mcp-Name HTTP header | not set | set on task-creating responses (SEP-2243) |

After `tasks/cancel`, observe the resulting `cancelled` status via the next `tasks/get`.

### 6. Status notification method renamed

| | v1 | v2 |
|---|---|---|
| Status notification method | `notifications/tasks/status` | `notifications/tasks` |

Payload shape is unchanged on the v2 path (still a `DetailedTask` carrying the SEP-2322 `requestState`). Only the JSON-RPC method name moved. The v1 method name is preserved, so hybrid servers emit both names — v2 clients subscribe to `notifications/tasks`, v1 clients keep their existing `notifications/tasks/status` subscription. (Spec commit `1d3813ab` in PR 2663.)

## Server migration paths

### Pure v1 server (no change required)

```go
srv := server.NewServer(info)
server.RegisterTasksV1(server.TasksConfigV1{Server: srv})
```

That's it. v1 stays where it always was — `server/tasks_v1.go`.

### Pure v2 server

```go
import (
    "github.com/panyam/mcpkit/server"
    "github.com/panyam/mcpkit/ext/tasks"
)

srv := server.NewServer(info)
tasks.Register(tasks.Config{Server: srv})
```

The v2 task surface lives at `github.com/panyam/mcpkit/ext/tasks` (separate go.mod sub-module, mirrors `ext/auth` and `ext/ui`). v2 server gates `tools/call` task creation on the client declaring the `io.modelcontextprotocol/tasks` extension. v1 clients (no declaration) see synchronous `tools/call` responses; `tasks/*` returns `-32601`.

### v1 + v2 on the same endpoint (no longer a single helper)

`RegisterTasksHybrid` was removed when v2 moved to `ext/tasks/`. The hybrid helper relied on access to v1's unexported internals (`taskMiddleware`, `makeGetHandler`, etc.), which would have required exporting them just for hybrid; the project chose not to.

Servers needing both surfaces during a rolling upgrade window should register independently:

```go
import (
    "github.com/panyam/mcpkit/server"
    "github.com/panyam/mcpkit/ext/tasks"
)

srv := server.NewServer(info)
server.RegisterTasksV1(server.TasksConfigV1{Server: srv})
tasks.Register(tasks.Config{Server: srv})
```

Caveat: `srv.HandleMethod` uses last-write-wins for `tasks/get` and `tasks/cancel` (both v1 and v2 register handlers for those slots). The example above registers v1 first and v2 second, so v2 wins. v1-only paths (`tasks/result`, `tasks/list`) continue to work. Per-request capability-aware dispatch (the old hybrid's behaviour) is not provided post-move; a v1 client sending a `task` hint to this server will hit the v2 handler, which doesn't know about the v1 `task` hint and falls through to sync execution. If your deployment needs per-request dispatch, do it at the transport / load-balancer layer instead.

## Client migration paths

### v1 client (no change required, just rename)

```go
// Was: client.ToolCallAsTask, client.GetTask, client.WaitForTask, ...
created, _ := client.ToolCallAsTaskV1(c, "tool", args)
got, _    := client.GetTaskV1(c, created.Task.TaskID)
final, _  := client.WaitForTaskV1(ctx, c, created.Task.TaskID, 200*time.Millisecond)
```

### v2 client (new code)

```go
c := client.NewClient(url, info,
    client.WithTasksExtension(), // declare the extension during initialize
)
defer c.Close()
c.Connect()

// Polymorphic tools/call
res, _ := client.ToolCall(c, "tool", args)
if res.IsTask() {
    final, _ := client.WaitForTask(ctx, c, res.Task.TaskID)
    // final.Result has the inlined ToolResult (or final.Error / final.InputRequests)
} else {
    // res.Sync is the regular *core.ToolResult
}

// MRTR resume
client.UpdateTask(c, core.UpdateTaskRequest{
    TaskID: taskID,
    InputResponses: core.InputResponses{ "elicit-1": json.RawMessage(`{...}`) },
    RequestState: requestState,
})

// Ack-only cancel; observe via WaitForTask
client.CancelTask(c, taskID)
```

`WaitForTask` honors the server's `pollIntervalMs` hint (with a 1s floor and 30s ceiling), and threads `requestState` echo automatically across iterations. To abort the loop the moment you call `CancelTask`, derive a child context with `context.WithCancel`, pass it as the `ctx` argument, and cancel it after `CancelTask` returns. `WaitForTask` exits with `context.Canceled` rather than waiting for the server to surface `cancelled` status.

## Rolling-upgrade recipe

The expected flow for a deployment migrating from v1 to v2 without downtime:

1. **Server**: install both surfaces side-by-side. Call `server.RegisterTasksV1(...)` then `tasks.Register(...)`. v1 clients hit the v1 paths (`tasks/result`, `tasks/list`, and `tasks/get` if no extension is declared — though see the dispatch caveat in the "v1 + v2 on the same endpoint" section above).
2. **Clients**: roll out the v2-aware client one cohort at a time. Each upgraded client adds `client.WithTasksExtension()` and switches to the v2 helpers.
3. Once the v1 client population is empty, drop the `server.RegisterTasksV1(...)` line. Only `tasks.Register(...)` remains.

## Known behaviours after SEP-2663 merge

SEP-2663 was merged Final at spec commit `c47bd846` on 2026-05-15. The merge picked up several normative clarifications that mcpkit had partially anticipated. The notes below capture how mcpkit lands against each.

### Schema categories removed (doc-site only)

The spec dropped a set of MDX `@category` markers (`notifications/tasks/status`, `tasks`, `tasks/get`, `tasks/result`, `tasks/list`, `tasks/cancel`, `tasks/input_response`) in spec commit `304aa7bf`. These were documentation-site-only constructs and never appeared as runtime constants in mcpkit. No-op for the implementation; `tasks/input_response` in particular was a never-shipped method name.

### notifications/cancelled does not cancel tasks

The spec clarified (commits `3f33c7d1` and `46394d21`) that `notifications/cancelled` applies to in-flight `tools/call` cancellation, not to task lifecycle. v2 task cancellation goes through `tasks/cancel` (`server/tasks_v2.go` `makeV2CancelHandler`); the existing `notifications/cancelled` handler in `server/dispatch.go` does not mutate task state. No code change required.

### notifications/progress and notifications/message disallowed on tasks

The spec hardened (commit `2dba297b`) to: "`notifications/progress` and `notifications/message` notifications MUST NOT be sent on the `subscriptions/listen` stream for a task, and are not supported on tasks in general." mcpkit enforces this at the session-notify boundary: the v2 task goroutine wraps its background context with `core.ApplySessionNotifyFilter` (defined in `core/background.go`) so a tool that calls `ToolContext.EmitProgress` or `BaseContext.EmitLog` while running as a task silently no-ops on those two methods. Tool authors do not need to know the rule; the framework drops the emissions.

### tasks/get response shapes per status

The spec added (commit `b15331ef`) five status-specific MUST rules for the `tasks/get` response shape: `working` returns the Task, `input_required` includes `inputRequests`, `completed` includes `result`, `cancelled` returns the Task, `failed` includes the error. mcpkit's `makeV2GetHandler` complies for the in-process execution path. External-backed tools (the planned `TaskCallbacks.OnInputResponse` extension point) are not yet wired and will need to surface the same fields once that path lands; tracked alongside the existing v2 callbacks work.

### Auth binding on every task-related request

The spec added (commit `527e5c5b`) the requirement that servers MUST authenticate and authorize each task-related request. mcpkit binds at two layers: the streamable transport's session-hijack protection binds `Claims.Subject` at session creation and re-verifies on each POST/GET/DELETE; the task handlers then scope every store lookup to the requesting session via `store.Get(taskID, sessionID)` / `store.Cancel(taskID, sessionID)`. Cross-session attempts surface as "task not found" rather than leaking task existence.

### Required-tasks return -32003 instead of silently downgrading to sync

The spec added the requirement that a server which cannot service a request without returning `CreateTaskResult` (i.e. a tool with `TaskSupport=required`) MUST return error `-32003` (Missing Required Client Capability) with a `data.requiredCapabilities` payload, rather than silently downgrading. mcpkit's `taskV2Middleware` now evaluates `TaskSupport` before checking extension declaration; required tools called by clients that have not declared `io.modelcontextprotocol/tasks` get `-32003` with a structured payload. `TaskSupport=optional` retains the sync-fallback behaviour because the server can still service those without a task. The new error code is exported as `core.ErrCodeMissingRequiredClientCapability`.

### requestState removed from the tasks-v2 wire

The merged SEP-2663 dropped the `requestState?: string` field from the `Task` base interface and removed the entire "Request State Management" section. mcpkit's `core.DetailedTask`, `core.UpdateTaskRequest`, `tasks/get` inline param struct, and `tasks/cancel` inline param struct no longer carry the field; the runtime helpers (`v2TaskRuntime.makeRequestState` / `verifyRequestState`) and per-registration signing config (`TasksConfig.RequestStateKey` / `RequestStateTTL`) are removed. The client surface drops `TaskOptions.RequestState`; `client.GetTask` and `client.CancelTask` simplify to `(c, taskID)` signatures, and `WaitForTask` no longer threads requestState through its poll loop.

SEP-2322's `core.InputRequiredResult.RequestState` (the MRTR multi-round-trip surface) is unchanged. The server-wide `WithRequestStateSigning` option stays — MRTR's dispatcher still uses it via `s.dispatcher.mrtr` for signing the MRTR round state. The `core.SignRequestState` / `core.VerifyRequestState` helpers are retained because `server/mrtr.go` reads legacy single-round MRTR tokens with the older payload shape for backward compatibility; that shim is removable once in-flight rounds rotate past.

## Reference

- SEP-2663 (Tasks Extension): https://github.com/modelcontextprotocol/specification/pull/2663
- SEP-2322 (MRTR base types): https://github.com/modelcontextprotocol/specification/pull/2322
- SEP-2575 (per-request capabilities): pattern adopted from spec discussion
- SEP-2243 (Mcp-Name HTTP header): adopted from spec discussion
- Implementation plan + open questions: [`PLAN.md`](../PLAN.md)
- v2 example walkthrough: [`examples/tasks-v2/WALKTHROUGH.md`](../examples/tasks-v2/WALKTHROUGH.md)
- v2 conformance suite: [panyam/mcpconformance — `src/scenarios/server/tasks/`](https://github.com/panyam/mcpconformance/tree/feat/tasks-mrtr-extension/src/scenarios/server/tasks) (8 ClientScenario classes / ~33 internal checks; upstream Draft PR modelcontextprotocol/conformance#262). Local sentinel for mcpkit-stricter scenarios: [`conformance/tasks-v2/`](../conformance/tasks-v2/).
