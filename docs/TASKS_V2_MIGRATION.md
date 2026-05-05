# Tasks v1 → v2 Migration Guide

This guide explains how to move an mcpkit server or client from the v1 task surface (MCP spec 2025-11-25) to the v2 surface (SEP-2663 Tasks Extension), and how to keep both running on the same server during a rolling transition.

> **Status:** v2 is in spec draft (SEP-2663) at the time of writing. The wire shapes implemented here track the spec; expect minor follow-ups as it finalizes. v1 stays frozen and supported.

## TL;DR

| You are | You want | Do |
|---|---|---|
| Building a new server today | v2 only | `server.RegisterTasks(server.TasksConfig{Server: srv})` |
| Maintaining an existing v1 server | Keep working v1 clients alive | `server.RegisterTasksV1(server.TasksConfigV1{Server: srv})` (no change — `RegisterTasks` was renamed) |
| Migrating a v1 server to v2 | Both clients on one endpoint | `server.RegisterTasksHybrid(server.TasksHybridConfig{Server: srv})` |
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
{ "resultType": "task", "taskId": "...", "status": "working", "ttlSeconds": 60, "pollIntervalMilliseconds": 1000 }
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
  "ttlSeconds": 60,
  "pollIntervalMilliseconds": 1000,
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
| TTL field | `ttl` (units by convention) | `ttlSeconds` (units in the name) |
| Poll-interval field | `pollInterval` | `pollIntervalMilliseconds` |
| `parentTaskId` | present | removed |
| Mcp-Name HTTP header | not set | set on task-creating responses (SEP-2243) |

After `tasks/cancel`, observe the resulting `cancelled` status via the next `tasks/get`.

## Server migration paths

### Pure v1 server (no change required)

```go
srv := server.NewServer(info)
// Was: server.RegisterTasks(server.TasksConfig{Server: srv})
server.RegisterTasksV1(server.TasksConfigV1{Server: srv})
```

That's it. The names changed but the shapes are identical to before.

### Pure v2 server

```go
srv := server.NewServer(info)
server.RegisterTasks(server.TasksConfig{Server: srv})
```

V2 server gates `tools/call` task creation on the client declaring the `io.modelcontextprotocol/tasks` extension. V1 clients (no declaration) see synchronous `tools/call` responses; `tasks/*` returns `-32601`.

### Hybrid server (v1 + v2 on the same endpoint)

Use this during a rolling client upgrade — keeps existing v1 clients working while new v2 clients land:

```go
srv := server.NewServer(info)
server.RegisterTasksHybrid(server.TasksHybridConfig{
    Server: srv,
    // V1 + V2 sub-configs are optional; defaults match the standalone helpers.
})
```

The hybrid server:

- Advertises **both** `capabilities.tasks` and `capabilities.extensions["io.modelcontextprotocol/tasks"]`.
- Routes per-request: `tools/call` and `tasks/get` / `tasks/cancel` dispatch to v2 when the client negotiated the extension, else v1.
- `tasks/update` is v2-only.
- `tasks/result` and `tasks/list` are v1-only — v2 clients hitting them get `-32601`.
- A client that declares both AND sends a `task` hint gets v2 (the modern path wins).

There's a small per-request dispatching cost; if you only need one path, prefer `RegisterTasks` or `RegisterTasksV1`.

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

`WaitForTask` honors the server's `pollIntervalMilliseconds` hint (with a 1s floor and 30s ceiling), and threads `requestState` echo automatically across iterations.

## Rolling-upgrade recipe

The expected flow for a deployment migrating from v1 to v2 without downtime:

1. **Server**: switch from `RegisterTasksV1` to `RegisterTasksHybrid`. Existing v1 clients keep working.
2. **Clients**: roll out the v2-aware client one cohort at a time. Each upgraded client adds `client.WithTasksExtension()` and switches to the v2 helpers.
3. Once the v1 client population is empty, switch the server from `RegisterTasksHybrid` to `RegisterTasks` and remove the v1 sub-config.

## Reference

- SEP-2663 (Tasks Extension): https://github.com/modelcontextprotocol/specification/pull/2663
- SEP-2322 (MRTR base types): https://github.com/modelcontextprotocol/specification/pull/2322
- SEP-2575 (per-request capabilities): pattern adopted from spec discussion
- SEP-2243 (Mcp-Name HTTP header): adopted from spec discussion
- Implementation plan + open questions: [`PLAN.md`](../PLAN.md)
- v2 example walkthrough: [`examples/tasks-v2/WALKTHROUGH.md`](../examples/tasks-v2/WALKTHROUGH.md)
- v2 conformance suite: [panyam/mcpconformance — `src/scenarios/server/tasks/`](https://github.com/panyam/mcpconformance/tree/feat/tasks-mrtr-extension/src/scenarios/server/tasks) (8 ClientScenario classes / ~33 internal checks; upstream Draft PR modelcontextprotocol/conformance#262). Local sentinel for mcpkit-stricter scenarios: [`conformance/tasks-v2/`](../conformance/tasks-v2/).
