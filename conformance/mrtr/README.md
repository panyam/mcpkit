# MRTR Conformance — SEP-2322 ephemeral IncompleteResult flow

Verifies that an MCP server correctly implements the ephemeral, stateless
multi-round-trip pattern from [SEP-2322][sep-2322]: server returns
`IncompleteResult{inputRequests, requestState}`; client retries the same
request with `inputResponses` plus the echoed `requestState` until the
server returns a complete result.

[sep-2322]: https://github.com/modelcontextprotocol/specification/pull/2322

## Wire format note

Per the upstream conformance contract, the `result_type` discriminator is
**snake_case** (`result_type: "incomplete"`); everything else
(`inputRequests`, `inputResponses`, `requestState`) stays camelCase. The
mcpkit dispatch and types follow the same convention.

## Server fixture

The server under test must register the tools listed below. The
[`examples/mrtr/`](../../examples/mrtr/) fixture in this repo provides a
reference implementation that wires each tool to the
[`ctx.RequestInput`](../../core/handler_context.go) /
[`ctx.InputResponse`](../../core/handler_context.go) helpers — point any
external server at the same names to run this suite against it.

| Tool | Scenario | Behaviour |
|------|----------|-----------|
| `test_tool_with_elicitation` | mrtr-01 | round 1: ask `user_name` via `elicitation/create`; round 2: greet by name |
| `test_incomplete_result_sampling` | mrtr-02 | round 1: ask via `sampling/createMessage`; round 2: echo response |
| `test_incomplete_result_list_roots` | mrtr-03 | round 1: ask `roots/list`; round 2: list URIs |
| `test_incomplete_result_request_state` | mrtr-04 | round 1: emit `requestState`; round 2: validate echo, return `state-ok` |
| `test_incomplete_result_multiple_inputs` | mrtr-05 | round 1: emit elicitation + sampling + roots in one map |
| `test_incomplete_result_multi_round` | mrtr-06 | two rounds of elicitation, accumulating across `requestState` |
| `test_incomplete_result_elicitation` | mrtr-07 | re-request via new `IncompleteResult` when client sends a wrong key |

## Scenario coverage

| ID | Coverage | SEP |
|----|----------|-----|
| mrtr-01 | Basic elicitation round-trip — `result_type:"incomplete"` discriminator, `inputRequests.user_name` keyed map, complete on retry | 2322 |
| mrtr-02 | Sampling round-trip via `sampling/createMessage` | 2322 |
| mrtr-03 | Roots round-trip via `roots/list` | 2322 |
| mrtr-04 | `requestState` echo validates on round 2 (`state-ok`) | 2322 |
| mrtr-05 | Multiple inputRequests of different methods in one round | 2322 |
| mrtr-06 | Multi-round flow: server-side accumulation across rounds via `requestState` | 2322 |
| mrtr-07 | Wrong-key inputResponses → server re-requests rather than errors | 2322 |
| mrtr-08 (skipped) | MRTR → Tasks composition (final round returns `CreateTaskResult`) | 2322 + 2663 |

## Running locally

```bash
# from repo root — handles build + spawn + tear-down
make testconf-mrtr

# or manually against an already-running server
cd conformance && npm install
SERVER_URL=http://localhost:18093/mcp npx tsx --test mrtr/scenarios.test.ts
```

## About the skipped scenario

`mrtr-08` (MRTR → Tasks composition) is intentionally skipped. SEP-2663
commit 451f5e1 (Apr 30) made this flow normative, but the existing v2
task middleware creates a task BEFORE the handler runs, so it never sees
the handler's `IsIncomplete` signal. Resolving this is a design choice
(always-sync handler vs. handler-signalled async); see mcpkit issue 347
for the tradeoff and proposed implementation paths. The matching Go test
(`server/TestMRTR_TaskComposition_Skipped`) is also skipped; both
re-enable the moment the underlying work lands.
