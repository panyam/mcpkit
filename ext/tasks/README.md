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
        // ...
    },
)

tasks.Register(tasks.Config{Server: srv})
```

`tasks.Register` installs the v2 middleware that intercepts `tools/call` for task-eligible tools, registers the `tasks/get` / `tasks/update` / `tasks/cancel` method handlers (all gated on the client declaring the extension), and advertises the extension in the `initialize` response.

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
