# ext/tasks — SEP-2663 Tasks Extension

Go module for the v2 task surface defined by [SEP-2663](https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2663) (merged Final 2026-05-15). Provides server-directed async task execution registered as a protocol extension under `capabilities.extensions["io.modelcontextprotocol/tasks"]`.

## Why a sub-module

SEP-2663 defines tasks as an **extension**, not a top-level capability. ext/tasks mirrors the same pattern `ext/auth` and `ext/ui` use: a separate `go.mod` consumed by servers that opt into the extension. v1 (the legacy `capabilities.tasks` surface) stays in `server/` as `RegisterTasksV1` and continues to ship until decommissioned.

## Quickstart

```go
import (
    "github.com/panyam/mcpkit/server"
    "github.com/panyam/mcpkit/ext/tasks"
)

srv := server.NewServer(info)

srv.RegisterTool(
    core.ToolDef{
        Name:        "slow_compute",
        Description: "...",
        Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportOptional},
    },
    func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
        // Handlers whose work is genuinely async opt into the continuation
        // goroutine via the GoAsync sentinel — see "Handler pattern" below.
        if tasks.GetTaskContext(ctx) == nil {
            return core.ToolResult{GoAsync: true}, nil
        }
        // Real work runs here, with TaskContext available for TaskElicit /
        // TaskSample / SetStatus / progress emission under the G6 filter.
        return core.TextResult("done"), nil
    },
)

tasks.Register(tasks.Config{Server: srv})
```

`tasks.Register` installs the v2 middleware that intercepts `tools/call` for task-eligible tools, registers the `tasks/get` / `tasks/update` / `tasks/cancel` method handlers (all gated on the client declaring the extension), and advertises the extension in the `initialize` response.

## Handler pattern (SEP-2663 Option 2: GoAsync)

Per the 2026-05-19 design decision on issue 347, mcpkit's v2 middleware runs the handler **synchronously first** and then dispatches on what it returned. The handler chooses one of three shapes:

| Handler returns | Middleware does | When to use |
|---|---|---|
| `core.InputRequiredResult` via `ctx.RequestInput(...)` | Passes through unchanged; no task created | SEP-2322 MRTR round — gather input before deciding whether to escalate |
| `core.ToolResult{GoAsync: true}` | Mints a task, spawns continuation goroutine that re-invokes the handler with `TaskContext` attached, returns `CreateTaskResult` | Slow / blocking work, `TaskElicit` / `TaskSample` calls, progress emission that should be filtered |
| `core.ToolResult{...}` (no GoAsync, no IsInputRequired) | Wraps as a born-terminal task (`status: completed`, result stored, one `notifications/tasks` event fired), returns `CreateTaskResult` | Sync work that finishes immediately on a `TaskSupport=optional/required` tool |

The continuation goroutine re-invokes the same handler with a `TaskContext` accessible via `tasks.GetTaskContext(ctx)`. The handler typically branches on that:

```go
func myHandler(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
    if tasks.GetTaskContext(ctx) != nil {
        // Continuation: do the real work. TaskElicit / TaskSample / SetStatus
        // are all available; emissions are filtered per SEP-2663 G6.
        return doRealWork(ctx, req)
    }

    // Optional MRTR phase: gather input via ctx.RequestInput first.
    if ctx.InputResponse("user_name") == nil {
        return ctx.RequestInput(core.InputRequests{
            "user_name": core.InputRequest{Method: "elicitation/create", Params: ...},
        })
    }

    // MRTR complete; defer the rest to the continuation goroutine.
    return core.ToolResult{GoAsync: true}, nil
}
```

The `examples/mrtr` reference fixture's `test_tool_with_task` walks this exact pattern end-to-end (drives the matching `mrtr-tasks-composition` conformance scenario).

**G6 filter scope:** the SEP-2663 G6 session-notify filter (`notifications/progress` and `notifications/message` MUST NOT be sent on tasks) is installed only on the continuation goroutine's `bgCtx`. A sync-returning handler runs on the unfiltered POST ctx — it is responsible for not leaking those notifications itself.

## Surface

| Symbol | Purpose |
|---|---|
| `tasks.Register(tasks.Config)` | Canonical entry point. |
| `tasks.Config` | Holds `Server`, `Store`, `DefaultTTLMs`, `DefaultPollMs`. |
| `tasks.TaskContext` | Typed-context for tool handlers running as v2 tasks. Exposes `TaskID()`, `ProgressToken()`, `SetStatus(status)`, `TaskElicit(req)`, `TaskSample(req)`. |
| `tasks.WithTaskContext` / `tasks.GetTaskContext` | Context wiring (mirrors core's typed-context pattern). |

The wire types (`CreateTaskResult`, `DetailedTask`, `UpdateTaskRequest`, `TaskInfoV2`, etc.) live in `core/task_v2.go` since they're consumed by both server and client.

## Relationship to server/

- ext/tasks **uses** `server.Server`, `server.Middleware`, `server.MethodHandler`, `server.Registry`, `server.TaskStore`, `server.NewInMemoryStore`. These are the generic server primitives any extension consumes.
- ext/tasks **duplicates** v2-flavoured pieces (`TaskContext`, `activeTask`, `generateTaskID`) rather than sharing with v1's versions in `server/`. Rationale: v1 is frozen and decommed-bound; once it retires, deletion in `server/` is total without unwinding cross-package shared types.
- The previous `RegisterTasksHybrid` (a single helper installing both v1 and v2 on one server) was dropped during the move. See [`docs/TASKS_V2_MIGRATION.md`](../../docs/TASKS_V2_MIGRATION.md) for the recommended two-call pattern.

## Where the conformance suite lives

[`panyam/mcpconformance`](https://github.com/panyam/mcpconformance/tree/feat/tasks-mrtr-extension/src/scenarios/server/tasks) on the `feat/tasks-mrtr-extension` branch. Run via `make testconf-tasks-v2` in the root repo — it spawns `examples/tasks-v2/tasks-v2 --serve` as the reference implementation fixture.

## V1-RETIREMENT markers

Code that should be reconsidered when v1 retires is tagged `V1-RETIREMENT:` in comments. `git grep V1-RETIREMENT` enumerates the cleanup list at retirement time.
