# MCP Tasks Conformance Suite

Tests any MCP server that implements the Tasks protocol (spec 2025-11-25). Uses the official [MCP TypeScript SDK](https://github.com/modelcontextprotocol/typescript-sdk) client.

## Prerequisites

The target server MUST register these tools:

| Tool | Task Support | Behavior |
|------|-------------|----------|
| `greet` | forbidden (absent) | Returns `Hello, {name}!` |
| `slow_compute` | optional | Sleeps `seconds` seconds, returns result |
| `failing_job` | required | Fails after 1 second |
| `external_job` | required | Completes after 1s (uses TaskCallbacks for proxy pattern) |
| `confirm_delete` | required | Elicitation: asks user before deleting a file |
| `write_haiku` | required | Sampling: asks LLM to write a haiku on a topic |

Both the Go example (`examples/tasks/main.go`) and TS reference server (`examples/tasks/ts-reference-server.mjs`) provide these tools.

## Setup

```bash
cd conformance
npm install
```

## Usage

```bash
# Start your server
cd examples/tasks && go run . -addr :8080 &
# OR
cd examples/tasks && node ts-reference-server.mjs &

# Run conformance scenarios
SERVER_URL=http://localhost:8080/mcp npx tsx --test tasks/scenarios.test.ts
```

## Scenarios

27 scenarios covering the full Tasks v1 protocol surface.

### Core Lifecycle

| # | Scenario | What it tests | Expected |
|---|----------|---------------|----------|
| 01 | Sync tool call | `greet` returns immediately, no task | `content[0].text` = `"Hello, World!"` |
| 02 | Async task creation | `tools/call` + task hint → CreateTaskResult | Non-terminal status, `taskId` and `createdAt` present |
| 03 | Poll task status | `tasks/get` returns flat TaskInfo | `taskId` and `status` at root level |
| 04 | Failing job | Tool error → task status: failed | Terminal status = `failed` within 10s |
| 05 | Task result | `tasks/result` returns ToolResult + related-task meta | `_meta["io.modelcontextprotocol/related-task"].taskId` matches |
| 06 | Task cancellation | `tasks/cancel` → status: cancelled | Cancel returns `cancelled`; subsequent get confirms |
| 07 | Task list | `tasks/list` returns array (capability-conditional) | `tasks` is a non-empty array |

### Error Handling

| # | Scenario | What it tests | Expected |
|---|----------|---------------|----------|
| 08 | Required without hint | Required tool called without task hint | JSON-RPC error (code varies by impl) |
| 09 | Forbidden with hint | Forbidden tool called with task hint | JSON-RPC error (code varies by impl) |
| 13 | Get non-existent task | `tasks/get` with bogus taskId | JSON-RPC error (code varies by impl) |
| 14 | Cancel non-existent task | `tasks/cancel` with bogus taskId | JSON-RPC error (code varies by impl) |
| 15 | Cancel completed task | Cancel after task finishes | JSON-RPC error (code varies by impl) |

> **Note on error codes:** The spec does not mandate specific JSON-RPC error codes for
> these cases. Go uses `-32601`/`-32602`; TS SDK uses `-32603`. The conformance suite
> verifies an error with a numeric code is returned, not a specific code.

### External Proxy (TaskCallbacks)

| # | Scenario | What it tests | Expected |
|---|----------|---------------|----------|
| 10 | External proxy lifecycle | `external_job` completes via TaskCallbacks path | Completes with `_meta.related-task.taskId` matching |
| 11 | External proxy tasks/get | `external_job` tasks/get returns valid state | `status` is `working` or `completed` |

### Task Configuration

| # | Scenario | What it tests | Expected |
|---|----------|---------------|----------|
| 12 | Optional tool sync | `slow_compute` without task hint runs synchronously | Inline result, no `task` field |
| 16 | TTL in response | CreateTaskResult includes a TTL | `ttl` present and positive (server may differ from client hint) |
| 17 | pollInterval in response | CreateTaskResult includes a pollInterval | `pollInterval` present and positive (server-provided, not client param) |
| 18 | TTL — no early expiry | Task must not expire before TTL | Task accessible well before TTL elapses |
| 21 | Execution in tools/list | `tools/list` includes `execution.taskSupport` | `optional`, `required`, absent per tool |

> **Note on TTL/pollInterval:** The client's `task.ttl` is a statement of intent — the
> server MAY use a different value. `pollInterval` is a server response field, not a client
> request parameter (the TS SDK's client-side `pollInterval` param was a bug). The suite
> only verifies these fields are present and valid, not that they match client hints.

### Concurrency

| # | Scenario | What it tests | Expected |
|---|----------|---------------|----------|
| 19 | Concurrent creation | Create 5 tasks simultaneously | All get unique taskIds, all complete |

### Result Semantics

| # | Scenario | What it tests | Expected |
|---|----------|---------------|----------|
| 20 | Failed task result | `tasks/result` after `failing_job` | `isError: true` |

### Side-Channel (Elicitation & Sampling)

| # | Scenario | What it tests | Expected |
|---|----------|---------------|----------|
| 22 | Elicitation round-trip | `confirm_delete` → `input_required` → elicitation via `tasks/result` → completed | Result confirms deletion; task completed |
| 23 | Sampling round-trip | `write_haiku` → `input_required` → sampling via `tasks/result` → completed | Result contains haiku; task completed |

### Notifications (optional)

| # | Scenario | What it tests | Expected |
|---|----------|---------------|----------|
| 24 | Progress notifications | `slow_compute` emits progress per second | Well-formed if received (not required) |
| 25 | Status notifications | Status change on completion | Well-formed and matches task state if received (not required) |

> **Note on notifications:** Progress and status notifications are optional per spec.
> The suite verifies they are well-formed *if* received — it does not require the server
> to send them. If notifications are sent, they must match the actual task state at that
> moment.

### Related-Task Metadata

| # | Scenario | What it tests | Expected |
|---|----------|---------------|----------|
| 26 | related-task on tasks/result | `tasks/result` includes `_meta["io.modelcontextprotocol/related-task"]` | `taskId` matches |
| 27 | No related-task on tasks/get | `tasks/get` SHALL NOT include related-task meta | `_meta` absent or no related-task key |

## Not Tested

The following aspects are **intentionally not covered** by this conformance suite:

| Topic | Why |
|-------|-----|
| Authorization-context binding | Identity isn't formally defined in the spec. TaskStore enforces auth-context binding, but with sessions becoming optional there's no portable test without a real identity model. Each server's auth binding is clear to its own author, but not testable across all servers. |
| Specific error codes | The spec does not mandate error codes for task operations. Different implementations use different codes (-32601, -32602, -32603). |
| TTL post-expiry enforcement | Servers MAY expire tasks at any point after TTL — not necessarily immediately. Only pre-TTL existence is testable. |

## Cross-Server Compatibility

All 27 scenarios pass against both:
- **Go server** (`examples/tasks/main.go`) — mcpkit
- **TS reference server** (`examples/tasks/ts-reference-server.mjs`) — official MCP TypeScript SDK

Known behavioral differences (both spec-compliant):
- **Initial status for elicitation/sampling tools**: Go returns `working` (goroutine hasn't run yet); TS returns `input_required` (async fires before response flush). Scenarios 22-23 accept both.
- **Error codes**: Go uses `-32601`/`-32602`; TS SDK uses `-32603`. Suite checks for any numeric code.
- **pollInterval**: Go respects client hint; TS SDK uses its own default. Suite checks for any positive number.
