# MCP Tasks v2 (SEP-2557) — Server-Directed Async

Server-side implementation of the v2 Tasks protocol. v2 inverts v1's client-driven model: the *server* decides when to create a task, and clients call `tools/call` normally with no task hint.

> **🚀 [Skip to the guided walkthrough →](WALKTHROUGH.md)** — 7-step demokit walkthrough with sequence diagram contrasting v1 vs v2 semantics: server-decided tasks, `resultType` discriminator, inlined results, and tool-vs-protocol error semantics. Run it with `make serve` + `make demo`.

## Key Differences from v1

| Aspect | v1 (SEP-1036) | v2 (SEP-2557) |
|--------|---------------|---------------|
| Client task hint | `task: {ttl, pollInterval}` in params | **none — server decides** |
| Discriminator on `tools/call` | absent (use `taskId` presence) | `resultType: "task"` |
| Read endpoints | `tasks/get` + `tasks/result` (two RTTs) | `tasks/get` only (result inlined) |
| Result on terminal `tasks/get` | only status — fetch separately | inlined `result` / `error` / `inputRequests` |
| TTL units | milliseconds | **seconds** |
| Tool errors | `status: failed` | `status: completed, result.isError: true` |
| Protocol errors | `status: failed, error: ...` | `status: failed, error: {code, message, data}` |
| `tasks/list` | exists | **removed** |

## Quick Start

```bash
make serve   # terminal 1: v2 tasks server on :8080
make demo     # terminal 2: demokit walkthrough (7 steps)
```

See [WALKTHROUGH.md](WALKTHROUGH.md) for the full step-by-step description and sequence diagram.

## Tools

| Tool | TaskSupport | What it demonstrates |
|------|-------------|---------------------|
| `greet` | forbidden | Sync-only — server returns ToolResult directly, no `resultType` |
| `slow_compute` | optional | Server creates a task; client gets `resultType: "task"` discriminator |
| `failing_job` | required | Tool error path → terminal `completed` + `isError: true` |
| `protocol_error_job` | required | Protocol error path → terminal `failed` + `error: {...}` |
| `external_job` | required | TaskCallbacks proxy pattern (external task store) |

## Conformance

- 21 scenarios in `conformance/tasks-v2/scenarios.test.ts` (Node + tsx)
- Implementation plan: [`docs/TASKS_V2_PLAN.md`](../../docs/TASKS_V2_PLAN.md)
- Spec: [SEP-2557](https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2557)

## Related

- v1 example: [`examples/tasks/`](../tasks/)
- Server library: [`server/tasks_v2.go`](../../server/tasks_v2.go)
- Wire types: [`core/task_v2.go`](../../core/task_v2.go)
