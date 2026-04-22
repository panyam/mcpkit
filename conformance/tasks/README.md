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

| # | Scenario | What it tests | Expected |
|---|----------|---------------|----------|
| 01 | Sync tool call | greet returns immediately, no task | `content[0].text` = `"Hello, World!"` |
| 02 | Async task creation | tools/call + task hint → CreateTaskResult | `task.status` = `working`, `taskId` and `createdAt` present |
| 03 | Poll task status | tasks/get returns flat TaskInfo | `taskId` and `status` at root level |
| 04 | Failing job | Tool error → task status: failed | Terminal status = `failed` within 10s |
| 05 | Task result | tasks/result returns ToolResult + related-task meta | `_meta["io.modelcontextprotocol/related-task"].taskId` matches |
| 06 | Task cancellation | tasks/cancel → status: cancelled | Cancel returns `cancelled`; subsequent get confirms |
| 07 | Task list | tasks/list returns array | `tasks` is a non-empty array |
| 08 | Required without hint | Error when required tool called without task hint | JSON-RPC error (message or code present) |
| 09 | Forbidden with hint | Error when forbidden tool called with task hint | JSON-RPC error (message or code present) |
| 10 | External proxy lifecycle | external_job completes via TaskCallbacks path | Completes with `_meta.related-task.taskId` matching |
| 11 | External proxy tasks/get | external_job tasks/get returns valid state | `status` is `working` or `completed` |

## Future scenarios

- Session isolation (needs two concurrent clients)
- TTL expiry (needs wait + poll)
- Status notifications (needs SSE listener)
- Progress token round-trip
- Elicitation / sampling via side-channel
