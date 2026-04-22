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

26 scenarios covering the full Tasks v1 protocol surface.

### Core Lifecycle

| # | Scenario | What it tests | Expected |
|---|----------|---------------|----------|
| 01 | Sync tool call | `greet` returns immediately, no task | `content[0].text` = `"Hello, World!"` |
| 02 | Async task creation | `tools/call` + task hint → CreateTaskResult | `task.status` = `working`, `taskId` and `createdAt` present |
| 03 | Poll task status | `tasks/get` returns flat TaskInfo | `taskId` and `status` at root level |
| 04 | Failing job | Tool error → task status: failed | Terminal status = `failed` within 10s |
| 05 | Task result | `tasks/result` returns ToolResult + related-task meta | `_meta["io.modelcontextprotocol/related-task"].taskId` matches |
| 06 | Task cancellation | `tasks/cancel` → status: cancelled | Cancel returns `cancelled`; subsequent get confirms |
| 07 | Task list | `tasks/list` returns array | `tasks` is a non-empty array |

### Error Handling

| # | Scenario | What it tests | Expected |
|---|----------|---------------|----------|
| 08 | Required without hint | Required tool called without task hint | JSON-RPC error |
| 09 | Forbidden with hint | Forbidden tool called with task hint | JSON-RPC error |
| 13 | Get non-existent task | `tasks/get` with bogus taskId | JSON-RPC error |
| 14 | Cancel non-existent task | `tasks/cancel` with bogus taskId | JSON-RPC error |
| 15 | Cancel completed task | Cancel after task finishes | JSON-RPC error |

### External Proxy (TaskCallbacks)

| # | Scenario | What it tests | Expected |
|---|----------|---------------|----------|
| 10 | External proxy lifecycle | `external_job` completes via TaskCallbacks path | Completes with `_meta.related-task.taskId` matching |
| 11 | External proxy tasks/get | `external_job` tasks/get returns valid state | `status` is `working` or `completed` |

### Task Configuration

| # | Scenario | What it tests | Expected |
|---|----------|---------------|----------|
| 12 | Optional tool sync | `slow_compute` without task hint runs synchronously | Inline result, no `task` field |
| 16 | TTL passthrough | Client sends `task.ttl`, reflected in CreateTaskResult | `task.ttl` matches hint |
| 17 | Poll interval passthrough | Client sends `task.pollInterval` | `task.pollInterval` is a positive number |
| 18 | TTL expiry | Create with short TTL, wait, poll | `task not found` after TTL expires |
| 22 | Execution in tools/list | `tools/list` includes `execution.taskSupport` | `optional`, `required`, absent per tool |

### Concurrency & Isolation

| # | Scenario | What it tests | Expected |
|---|----------|---------------|----------|
| 19 | Concurrent creation | Create 5 tasks simultaneously | All get unique taskIds, all complete |
| 21 | Session isolation | Task from client A not visible to client B | Client B gets error on A's taskId |

### Result Semantics

| # | Scenario | What it tests | Expected |
|---|----------|---------------|----------|
| 20 | Failed task result | `tasks/result` after `failing_job` | `isError: true` or failure text in content |

### Side-Channel (Elicitation & Sampling)

| # | Scenario | What it tests | Expected |
|---|----------|---------------|----------|
| 23 | Elicitation round-trip | `confirm_delete` → `input_required` → elicitation via `tasks/result` → completed | Result confirms deletion; task completed |
| 24 | Sampling round-trip | `write_haiku` → `input_required` → sampling via `tasks/result` → completed | Result contains haiku; task completed |

### Notifications

| # | Scenario | What it tests | Expected |
|---|----------|---------------|----------|
| 25 | Progress notifications | `slow_compute` emits progress per second | Client receives ≥1 progress event with numeric `progress` |
| 26 | Status notifications | Status change on completion | Client receives `notifications/tasks/status` for the task |

## Cross-Server Compatibility

All 26 scenarios pass against both:
- **Go server** (`examples/tasks/main.go`) — mcpkit
- **TS reference server** (`examples/tasks/ts-reference-server.mjs`) — official MCP TypeScript SDK

Known behavioral differences (both spec-compliant):
- **Initial status for elicitation/sampling tools**: Go returns `working` (goroutine hasn't run yet); TS returns `input_required` (async fires before response flush). Scenarios 23-24 accept both.
- **Poll interval passthrough**: Go respects client hint; TS SDK defaults to 1000ms. Scenario 17 checks for any positive number.
