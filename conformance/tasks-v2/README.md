# MCP Tasks v2 Conformance Suite (SEP-2557)

Tests any MCP server that implements the Tasks v2 protocol as proposed in [SEP-2557](https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2557). Uses the official [MCP TypeScript SDK](https://github.com/modelcontextprotocol/typescript-sdk) client.

**SEP Status:** Draft, targeted for 2026-06-30-RC milestone.

These tests are written ahead of the spec being finalized — they will evolve as the SEP is refined. All scenarios are expected to **fail** until a v2 server implementation exists.

## What changed from v1

| Aspect | v1 (spec 2025-11-25) | v2 (SEP-2557) |
|--------|----------------------|---------------|
| Task creation | Client sends `task` param | Server decides unilaterally |
| `tasks/get` | Returns status only | Returns status + inlined result/error/inputRequests |
| `tasks/result` | Separate blocking method | **Removed** |
| `tasks/list` | Session-scoped list | **Removed** |
| `tasks/cancel` | Optional | **Required** |
| TTL units | Milliseconds | **Seconds** |
| `requestState` | N/A | Opaque server state, echoed by client |
| Input handling | Side-channel via `tasks/result` | Inline `inputRequests`/`inputResponses` in `tasks/get` |
| Capabilities | Negotiated via `TasksCap` | **Removed** — tasks are core protocol |
| `execution.taskSupport` | Per-tool field | **Removed** |

## Prerequisites

The target server MUST register these tools:

| Tool | Behavior |
|------|----------|
| `greet` | Sync-only — returns `Hello, {name}!` |
| `slow_compute` | Async — sleeps `seconds` seconds, returns result |
| `failing_job` | Async — always fails after 1 second |
| `confirm_delete` | Async — elicitation via inputRequests model |

## Setup

```bash
cd conformance
npm install
```

## Usage

```bash
# Start a v2 server (not yet implemented)
# SERVER_URL=http://localhost:8080/mcp npx tsx --test tasks-v2/scenarios.test.ts
```

## Scenarios

18 scenarios covering the Tasks v2 protocol surface.

### Core Lifecycle

| # | Scenario | What it tests | Expected |
|---|----------|---------------|----------|
| 01 | Sync tool call | `greet` returns immediately, no task | Inline result, no `task` field |
| 02 | Server-directed task creation | Server creates task without client `task` param | `task.taskId` present, non-terminal status |
| 03 | tasks/get working status | Poll active task | `status: working` or `completed` |
| 04 | tasks/get completed + inlined result | Poll completed task | `status: completed` + `result` with content |
| 05 | tasks/get failed + inlined error | Poll failed task | `status: failed` + `error` field |
| 06 | tasks/cancel (required) | Cancel working task | `status: cancelled` |
| 07 | Cancel terminal task | Cancel completed task | JSON-RPC error |

### Removed v1 Methods

| # | Scenario | What it tests | Expected |
|---|----------|---------------|----------|
| 08 | No tasks/result | Server rejects `tasks/result` | `-32601` MethodNotFound |
| 09 | No tasks/list | Server rejects `tasks/list` | `-32601` MethodNotFound |

### TTL (seconds)

| # | Scenario | What it tests | Expected |
|---|----------|---------------|----------|
| 10 | TTL in seconds | TTL value is reasonable for seconds (not ms) | `ttl < 10000` |
| 11 | No early expiry | Task exists before TTL elapses | Task accessible |

### requestState

| # | Scenario | What it tests | Expected |
|---|----------|---------------|----------|
| 12 | Server returns requestState | tasks/get may include requestState | String if present |
| 13 | Client echoes requestState | Subsequent tasks/get includes server's requestState | Server accepts |

### Input Handling (MRTR)

| # | Scenario | What it tests | Expected |
|---|----------|---------------|----------|
| 14 | inputRequests in tasks/get | `input_required` status has `inputRequests` array | Non-empty array of request objects |
| 15 | inputResponses resumes task | Client sends responses, task completes | Task transitions to working/completed |

> **Note:** There is an active debate (April 2026) about whether `inputResponses` should
> live on `tasks/get` or a separate `tasks/continue` method. Scenarios 14-15 use `tasks/get`
> per the current SEP text. Will update if the spec changes.

### Notifications

| # | Scenario | What it tests | Expected |
|---|----------|---------------|----------|
| 16 | DetailedTask in notifications | Terminal notification includes inlined result | Well-formed if received (optional) |

### Task Creation Semantics

| # | Scenario | What it tests | Expected |
|---|----------|---------------|----------|
| 17 | No client `task` param | tools/call without `task` still creates task | CreateTaskResult returned |
| 18 | Immediate result shortcut | Fast operation may skip task creation | CallToolResult or CreateTaskResult (both valid) |

## Design Notes

### Assertions follow v1 lessons

Based on spec maintainer feedback on the v1 suite:
- Error codes use `assertJsonRpcError(e, code, label, enforce?)` with `ENFORCE_ERROR_CODES = false` by default
- `enforce = true` only for cases where the code is mandated (e.g., `-32601` for removed methods)
- TTL assertions check reasonable ranges, not exact values
- Notifications are optional — well-formed if received
- No session/auth-context isolation test (identity undefined)

### Shared helpers

Common utilities (`assertJsonRpcError`, `waitForTerminal`, `waitForStatus`) are in `conformance/common/helpers.ts` and shared with the v1 suite.

### Not in CI

This suite is **not** wired into `make testall` or pre-push hooks. No v2 server exists yet. Run manually when testing a v2 implementation:

```bash
SERVER_URL=http://localhost:8080/mcp npx tsx --test tasks-v2/scenarios.test.ts
```

A `make testconf-tasks-v2` target will be added when a v2 server is available.
