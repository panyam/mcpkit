# MCP Tasks Conformance Suite

Tests any MCP server that implements the Tasks protocol (spec 2025-11-25). Uses the official [MCP TypeScript SDK](https://github.com/modelcontextprotocol/typescript-sdk) client.

## Prerequisites

The target server MUST register these tools:

| Tool | Task Support | Behavior |
|------|-------------|----------|
| `greet` | forbidden (absent) | Returns `Hello, {name}!` |
| `slow_compute` | optional | Sleeps `seconds` seconds, returns result |
| `failing_job` | required | Fails after 1 second |

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

| # | Scenario | What it tests |
|---|----------|---------------|
| 01 | Sync tool call | greet returns immediately, no task |
| 02 | Async task creation | tools/call + task hint → CreateTaskResult |
| 03 | Poll task status | tasks/get returns flat TaskInfo |
| 04 | Failing job | Tool error → task status: failed |
| 05 | Task result | tasks/result returns ToolResult + related-task meta |
| 06 | Task cancellation | tasks/cancel → status: cancelled |
| 07 | Task list | tasks/list returns array |
| 08 | Required without hint | Error when required tool called without task hint |
| 09 | Forbidden with hint | Error when forbidden tool called with task hint |

## Future scenarios

- Session isolation (needs two concurrent clients)
- TTL expiry (needs wait + poll)
- Status notifications (needs SSE listener)
- Progress token round-trip
- Elicitation / sampling via side-channel
