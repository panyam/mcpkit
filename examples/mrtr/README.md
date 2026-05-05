# SEP-2322 MRTR — Ephemeral IncompleteResult Round-Trips

Demonstrates the SEP-2322 ephemeral Multi Round-Trip Requests pattern:
the server returns `IncompleteResult{inputRequests, requestState}` when
it needs more input from the client; the client resolves each
`inputRequest` (elicitation, sampling, roots) locally and retries the
SAME `tools/call` with `inputResponses` + the echoed `requestState`.

Spec: [SEP-2322](https://github.com/modelcontextprotocol/specification/pull/2322).

## Three terminal shapes for `tools/call`

| `resultType` | What it means | Client action |
|--------------|---------------|---------------|
| `"complete"` (or absent) | Sync `ToolResult` | done |
| `"task"` | `CreateTaskResult` (SEP-2663) | poll `tasks/get` |
| `"incomplete"` | `IncompleteResult{inputRequests, requestState}` | resolve inputs, retry the same call |

The discriminator is `resultType` — camelCase like every other MCP wire
field (`inputRequests`, `inputResponses`, `requestState`, `taskId`, …).

## Quick Start

```bash
# Terminal 1 — start the MCP server
make serve

# Terminal 2 — scripted walkthrough
make demo

# Or for the interactive TUI:
go run . --tui
```

The walkthrough wires canned `WithElicitationHandler` /
`WithSamplingHandler` / `WithRootsHandler` callbacks so the round-trip
runs end-to-end without user interaction. In production those callbacks
prompt the user, hit an LLM, or read filesystem roots.

See [WALKTHROUGH.md](WALKTHROUGH.md) for the full sequence diagram and
step-by-step description.

## What it demonstrates

- The full IncompleteResult round-trip — server returns `inputRequests + requestState`; client resolves each request locally and retries the same `tools/call` with `inputResponses` + the echoed state.
- All three input methods inline-able inside the round: elicitation (`elicitation/create`), sampling (`sampling/createMessage`), and root listing (`roots/list`).
- Multi-round accumulation across rounds via signed `requestState` — handlers stay stateless; dispatch merges accumulated answers.
- The unified client dispatch path (`HandleServerRequestWithContext`) handling MRTR-synthesized requests identically to real server-initiated requests.
- The `client.CallToolWithInputs` + `DefaultInputHandler` auto-loop that collapses the whole flow into a single call.

## Tools registered (matching the upstream conformance contract)

| Tool | Scenario | Behavior |
|------|----------|----------|
| `test_tool_with_elicitation` | A1 | round 1: ask `user_name`; round 2: greet by name |
| `test_incomplete_result_sampling` | A2 | round 1: ask via sampling; round 2: echo back |
| `test_incomplete_result_list_roots` | A3 | round 1: ask `roots/list`; round 2: list URIs |
| `test_incomplete_result_request_state` | A4 | round 1: emit requestState; round 2: validate echo |
| `test_incomplete_result_multiple_inputs` | A5 | round 1: emit elicitation + sampling + roots in one map |
| `test_incomplete_result_multi_round` | A6 | two rounds of elicitation, accumulating across `requestState` |
| `test_incomplete_result_elicitation` | A7 | re-request via new IncompleteResult on wrong-key inputResponses |

## Conformance fixture

The same binary is reused as the fixture for the
[`conformance/mrtr/`](../../conformance/mrtr/) suite (7 active scenarios
+ 1 deferred skip for MRTR↔Tasks composition). Run via `make
testconf-mrtr` from the repo root.

## Where to look in the code

| What | Where |
|------|-------|
| Server dispatch | [`server/dispatch.go`](../../server/dispatch.go) — handleToolsCall reshapes Incomplete; merges accumulated answers from `requestState` |
| Server runtime | [`server/mrtr.go`](../../server/mrtr.go) — `mrtrRuntime`, sign / verify / mint requestState tokens |
| Wire types | [`core.IncompleteResult`](../../core/task_v2.go), `MRTRRoundState`, `Sign|VerifyMRTRState` |
| Tool handler API | [`core/handler_context.go`](../../core/handler_context.go) — `ctx.RequestInput` sentinel + `InputResponse(key)` / `HasInputResponses()` / `RequestState()` accessors |
| Client auto-loop | [`client/mrtr.go`](../../client/mrtr.go) — `CallToolWithInputs` + `DefaultInputHandler` |
| Single dispatcher | [`client.HandleServerRequestWithContext`](../../client/client.go) — same switch handles real server-initiated requests AND MRTR-synthesized ones |
| Conformance | [`conformance/mrtr/scenarios.test.ts`](../../conformance/mrtr/scenarios.test.ts) |
| SEP | [SEP-2322 spec PR](https://github.com/modelcontextprotocol/specification/pull/2322) |
