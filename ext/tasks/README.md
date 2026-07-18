# ext/tasks — SEP-2663 Tasks Extension

Go module for the v2 task surface defined by [SEP-2663](https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2663) (merged Final 2026-05-15). Provides server-directed async task execution registered as a protocol extension under `capabilities.extensions["io.modelcontextprotocol/tasks"]`.

> **Looking for a guided walk-through?** Read [`docs/TASKS_TUTORIAL.md`](../../docs/TASKS_TUTORIAL.md) — covers when to use tasks vs sync vs MRTR, the GoAsync sentinel + middleware peek, lifecycle, the in-task input flow (`TaskElicit` / `TaskSample`), notifications + the G6 filter, cancellation semantics, and the multi-tenancy caveat on stateless. For the sibling MRTR surface (SEP-2322 multi-round-trip requests), see [`docs/MRTR_TUTORIAL.md`](../../docs/MRTR_TUTORIAL.md). This README is the API reference; the tutorials are the conceptual walk-throughs.

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
    func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
        // Handlers whose work is genuinely async opt into the continuation
        // goroutine by returning core.GoAsyncResult{} — see "Handler pattern" below.
        if tasks.GetTaskContext(ctx) == nil {
            return core.GoAsyncResult{}, nil
        }
        // Real work runs here, with TaskContext available for TaskElicit /
        // TaskSample / SetStatus / progress emission under the G6 filter.
        return core.TextResult("done"), nil
    },
)

tasks.Register(tasks.Config{Server: srv})
```

`tasks.Register` installs the v2 middleware that intercepts `tools/call` for task-eligible tools, registers the `tasks/get` / `tasks/update` / `tasks/cancel` method handlers (all gated on the client declaring the extension), and advertises the extension in the `initialize` response.

`ToolHandler` returns the sealed [`core.ToolResponse`](https://github.com/panyam/mcpkit/blob/main/core/tool.go) interface; the middleware dispatches on the concrete variant — `ToolResult`, `InputRequiredResult`, `CreateTaskResult`, or `GoAsyncResult`.

## Handler pattern

mcpkit's v2 middleware runs the handler **synchronously first** and then dispatches on the concrete `ToolResponse` variant it returned. The handler chooses one of three shapes:

| Handler returns | Middleware does | When to use |
|---|---|---|
| `core.InputRequiredResult` via `ctx.RequestInput(...)` | Passes through unchanged; no task created | SEP-2322 MRTR round — gather input before deciding whether to escalate |
| `core.GoAsyncResult{}` | Mints a task, spawns continuation goroutine that re-invokes the handler with `TaskContext` attached, returns `CreateTaskResult` | Slow / blocking work, `TaskElicit` / `TaskSample` calls, progress emission that should be filtered |
| `core.ToolResult{...}` (a regular sync result) | Wraps as a born-terminal task (`status: completed`, result stored, one `notifications/tasks` event fired), returns `CreateTaskResult` | Sync work that finishes immediately on a `TaskSupport=optional/required` tool |

The continuation goroutine re-invokes the same handler with a `TaskContext` accessible via `tasks.GetTaskContext(ctx)`. The handler typically branches on that:

```go
func myHandler(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
    if tasks.GetTaskContext(ctx) != nil {
        // Continuation: do the real work. TaskElicit / TaskSample / SetStatus
        // are all available; emissions are filtered per SEP-2663 G6.
        return doRealWork(ctx, req)
    }

    // Optional MRTR phase: gather input via ctx.RequestInput first.
    // ctx.RequestInput returns (core.InputRequiredResult, error) directly;
    // InputRequiredResult satisfies ToolResponse.
    if ctx.InputResponse("user_name") == nil {
        return ctx.RequestInput(core.InputRequests{
            "user_name": core.InputRequest{Method: "elicitation/create", Params: ...},
        })
    }

    // MRTR complete; defer the rest to the continuation goroutine.
    return core.GoAsyncResult{}, nil
}
```

The `examples/mrtr` reference fixture's `test_tool_with_task` walks this exact pattern end-to-end (drives the matching `mrtr-tasks-composition` conformance scenario). For the full conceptual walk-through — MRTR as a stateless continuation primitive, capabilities across wires, progressToken, the G6 filter replacement table, MRTR vs push vs task-input-flow — see [`docs/MRTR_TUTORIAL.md`](../../docs/MRTR_TUTORIAL.md).

**G6 filter scope:** the SEP-2663 G6 session-notify filter (`notifications/progress` and `notifications/message` MUST NOT be sent on tasks) is installed only on the continuation goroutine's `bgCtx`. A sync-returning handler runs on the unfiltered POST ctx — it is responsible for not leaking those notifications itself.

## Surface

| Symbol | Purpose |
|---|---|
| `tasks.Register(tasks.Config)` | Canonical entry point. |
| `tasks.Config` | Holds `Server`, `Store`, `DefaultTTLMs`, `DefaultPollMs`, `TracerProvider`. |
| `tasks.TaskContext` | Typed-context for tool handlers running as v2 tasks. Exposes `TaskID()`, `ProgressToken()`, `SetStatus(status)`, `TaskElicit(req)`, `TaskSample(req)`. |
| `tasks.WithTaskContext` / `tasks.GetTaskContext` | Context wiring (mirrors core's typed-context pattern). |

## Tracing (SEP-414 P6 — issue 659)

Optional. Set `Config.TracerProvider` to opt the runtime into span-link instrumentation of the async task lifecycle:

```go
tasks.Register(tasks.Config{
    Server:         srv,
    TracerProvider: mcpotel.NewProvider(otelTP), // or any core.TracerProvider
})
```

What gets emitted:

- **`task.execute` span on the GoAsync path** — a NEW root trace (not a child of the create span — the work outlives the `tools/call` dispatch span) carrying a `Link` back to the originating `tools/call` create span. Attributes: `mcp.task.id` (at start), `mcp.task.status` (stamped at End from the final stored status — `completed` / `failed` / `cancelled` / `input_required`). `RecordError` fires on protocol-level failures (mwErr, resp.Error, unexpected result shape, panic recover); handler-returned errors map to `completed` with `IsError=true` per SEP-2663 semantics.
- **`AddLink` on each `tasks/get` / `tasks/update` / `tasks/cancel` dispatch span** — points back to the originating create span so a backend can pivot from any poll into the whole lifecycle.

Nil or `core.NoopTracerProvider{}` (the default) skips the install — zero overhead, zero allocation. ext/tasks depends on `core` only; no compile-time dep on ext/otel. The contract details (`core.WithNewRootSpan`, `core.LinkedTracerProvider`, `core.Link`) live in `docs/SEP_414_OTEL.md` § Span links and § New-root-span marker.

The wire types (`CreateTaskResult`, `DetailedTask`, `UpdateTaskRequest`, `TaskInfoV2`, etc.) live in `core/task_v2.go` since they're consumed by both server and client.

## Relationship to server/

- ext/tasks **uses** `server.Server`, `server.Middleware`, `server.MethodHandler`, `server.Registry`, `server.TaskStore`, `server.NewInMemoryStore`. These are the generic server primitives any extension consumes.
- ext/tasks **duplicates** v2-flavoured pieces (`TaskContext`, `activeTask`, `generateTaskID`) rather than sharing with v1's versions in `server/`. Rationale: v1 is frozen and decommed-bound; once it retires, deletion in `server/` is total without unwinding cross-package shared types.
- The previous `RegisterTasksHybrid` (a single helper installing both v1 and v2 on one server) was dropped during the move. See [`docs/TASKS_V2_MIGRATION.md`](../../docs/TASKS_V2_MIGRATION.md) for the recommended two-call pattern.

## Where the conformance suite lives

[`panyam/mcpconformance`](https://github.com/panyam/mcpconformance/tree/feat/tasks-mrtr-extension/src/scenarios/server/tasks) on the `feat/tasks-mrtr-extension` branch. Run via `just testconf-tasks-v2` in the root repo — it spawns `examples/tasks-v2/tasks-v2 --serve` as the reference implementation fixture.

## V1-RETIREMENT markers

Code that should be reconsidered when v1 retires is tagged `V1-RETIREMENT:` in comments. `git grep V1-RETIREMENT` enumerates the cleanup list at retirement time.
