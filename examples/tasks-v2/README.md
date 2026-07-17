# MCP Tasks v2 (SEP-2663) — Server-Directed Async + MRTR

> **Stable** — implements [SEP-2663](https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2663) (Tasks v2), merged into the MCP spec.

Server-side implementation of the v2 Tasks extension. v2 inverts v1's client-driven model: the *server* decides when to create a task, and clients call `tools/call` normally with no task hint.

> **🚀 [Skip to the guided walkthrough →](WALKTHROUGH.md)** — 8-step demokit walkthrough covering the full v2 surface: extension negotiation, polymorphic `tools/call`, inlined results, the new `tasks/update` MRTR loop, ack-only cancel, and tool-vs-protocol error semantics. Run it with `make serve` + `make demo`.
>
> **🔁 Migrating from v1?** See the [v1 → v2 migration guide](../../docs/TASKS_V2_MIGRATION.md) for the wire-shape diff, server entry points (`tasks.Register` in `ext/tasks` / `server.RegisterTasksV1`), and the rolling-upgrade recipe.

## Key Differences from v1

| Aspect | v1 (SEP-1036) | v2 (SEP-2663) |
|--------|---------------|---------------|
| Capability slot | `capabilities.tasks` | `capabilities.extensions["io.modelcontextprotocol/tasks"]` |
| Client opt-in | (none) | `client.WithTasksExtension()` required |
| Client task hint | `task: {ttl, pollInterval}` in params | **none — server decides** |
| Discriminator on `tools/call` | absent (use `taskId` presence) | `resultType: "task"` |
| Read endpoints | `tasks/get` + `tasks/result` (two RTTs) | `tasks/get` only (result inlined) |
| Result on terminal `tasks/get` | only status — fetch separately | inlined `result` / `error` / `inputRequests` |
| MRTR resume path | side-channel via `tasks/result` long-poll | `tasks/update` (new method) |
| `tasks/cancel` response | rich task envelope | empty `{}` ack |
| TTL field | `ttl` (ms by convention) | `ttlMs` (integer milliseconds) |
| Poll-interval field | `pollInterval` | `pollIntervalMs` (integer milliseconds) |
| Tool errors | `status: failed` | `status: completed, result.isError: true` |
| Protocol errors | `status: failed, error: ...` | `status: failed, error: {code, message, data}` |
| `tasks/list` | exists | **removed** |
| Mcp-Name HTTP header | not set | set on task-creating responses (SEP-2243) |

## Quick Start

```bash
make serve   # terminal 1: v2 tasks server on :8080
make demo     # terminal 2: demokit walkthrough (7 steps)
```

See [WALKTHROUGH.md](WALKTHROUGH.md) for the full step-by-step description and sequence diagram.

## Agent mode

```bash
make agent                            # scripted agent, no LLM (also the golden test via make agent-test)
make agent-live MODEL=qwen2.5-7b-instruct   # a live model improvising against the same server
```

`make agent` runs a scripted agent (mcpkit's host layer plus a deterministic
`StubProvider`) against an in-process copy of this server, so it needs no second
terminal and no model. It shows the point of v2 from the *agent's* side: the
model calls a sync-only tool (`greet`) and a server-directed async tool
(`slow_compute`) the exact same way. The server alone decides `slow_compute`
runs as a task; the host creates it, runs it to completion, and hands the result
back as an ordinary tool result. The SEP-2663 task machinery never surfaces to
the model. The whole run is deterministic, so it doubles as a golden-transcript
test (`agent_scenario_test.go`).

When a task genuinely outlives its grace window the host detaches it and later
delivers a `task.completed` event, which a standing trigger can turn into a
proactive turn. That background-detach path is exercised in the agentchat tests
and the `agent-async` example; here the computation finishes fast to keep the
demo deterministic.

## What it demonstrates

- Tasks-as-extension negotiation — `client.WithTasksExtension()` opts in during initialize; servers gate task-creating `tools/call` and every `tasks/*` method on it.
- Polymorphic `tools/call` with the `resultType: "task"` discriminator — the *server* decides whether to create a task, no client hint required.
- Inlined results on terminal `tasks/get` — `result` / `error` / `inputRequests` arrive in one RTT, `tasks/result` removed.
- Tool-vs-protocol error semantics — tool errors land in `status: completed, isError: true`; protocol errors in `status: failed` with structured `error`.
- Empty-ack `tasks/cancel` plus follow-up `tasks/get` to observe the resulting `cancelled` status.
- The new `tasks/update` MRTR resume path closing the elicitation/sampling loop.
- The `Mcp-Name` HTTP response header (SEP-2243) carrying taskIds for observability without parsing the body.
- TaskCallbacks proxy pattern (external task store) via `external_job`.

## Tools

| Tool | TaskSupport | What it demonstrates |
|------|-------------|---------------------|
| `greet` | forbidden | Sync-only — server returns ToolResult directly, no `resultType` |
| `slow_compute` | optional | Server creates a task; client gets `resultType: "task"` discriminator |
| `failing_job` | required | Tool error path → terminal `completed` + `isError: true` |
| `protocol_error_job` | required | Protocol error path → terminal `failed` + `error: {...}` |
| `external_job` | required | TaskCallbacks proxy pattern (external task store) |

## Conformance

- Scenarios live in the [`panyam/mcpconformance`](https://github.com/panyam/mcpconformance) fork (branch `feat/tasks-mrtr-extension`, upstream Draft PR modelcontextprotocol/conformance#262). Run via `make testconf-tasks-v2` from the repo root — points the fork's vitest run at this binary.
- Migration guide: [`docs/TASKS_V2_MIGRATION.md`](../../docs/TASKS_V2_MIGRATION.md)
- Spec: [SEP-2663](https://github.com/modelcontextprotocol/specification/pull/2663)

## Where to look in the code

- v1 example: [`examples/tasks/`](../tasks/)
- Server library: [`ext/tasks/tasks.go`](../../ext/tasks/tasks.go)
- Wire types: [`core/task_v2.go`](../../core/task_v2.go)
- Client helpers: [`client/tasks.go`](../../client/tasks.go) (`ToolCall`, `GetTask`, `WaitForTask`, `UpdateTask`, `CancelTask`)
- Conformance scenarios: [panyam/mcpconformance — `src/scenarios/server/tasks/`](https://github.com/panyam/mcpconformance/tree/feat/tasks-mrtr-extension/src/scenarios/server/tasks)
- Local sentinel for mcpkit-stricter scenarios: [`conformance/tasks-v2/`](../../conformance/tasks-v2/)

## Next steps

- [MRTR — the input_required envelope tasks v2 reuses](../mrtr/)
- [Tasks tutorial](../../docs/TASKS_TUTORIAL.md)
