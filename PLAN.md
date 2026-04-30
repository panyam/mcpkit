# Plan: SEP-2322 MRTR IncompleteResult

**Issue:** mcpkit 341 | **Design review:** mcpkit 342
**Branch:** `feat/sep-2322-mrtr-incomplete` (from main)
**Depends on:** SEP-2663 work (merged) — resultType constants, InputRequest/InputResponses types, HMAC requestState

## Summary

Implement the ephemeral multi-round-trip flow from SEP-2322: server returns `IncompleteResult` with `inputRequests` instead of blocking, client retries with `inputResponses`. This is the lightweight alternative to Tasks for gathering input.

## Key decisions

- **Handler model: stateless restart (Option 1)** — handler is re-invoked on each retry, checks for `inputResponses`, returns `IncompleteResult` if input is missing. See mcpkit 342 for conditions under which to revisit.
- **Wire field: `result_type`** (snake_case) per conformance tests, NOT `resultType` (camelCase). Need to fix existing code too.
- **Scope: `tools/call` only** — conformance only tests tools/call. Other methods are future.
- **MRTR → Tasks composition** — MRTR loop can gather input, then final retry returns `CreateTaskResult`. Conformance tests validate this.

## Constraints check

| Constraint | Status |
|------------|--------|
| C1 (typed contexts) | OK — ToolContext already carries inputResponses |
| C2 (consolidated structs) | OK |
| C3 (no globals) | OK |
| server/C4 (no spec extensions without WG) | OK — implementing accepted SEP |

## Phase 1: Wire field rename + IncompleteResult type

**Files:** `core/task_v2.go`, `core/tool.go`

- [ ] Rename JSON tag `resultType` → `result_type` across all result types (breaking wire change — update all tests)
- [ ] Add `IncompleteResult` struct: `ResultType` ("incomplete"), `InputRequests map[string]InputRequest`, `RequestState string`
- [ ] Verify `InputRequest` struct already has `Method string` + `Params json.RawMessage` (done in SEP-2663)
- [ ] Add `InputResponses` field to `ToolRequest` params (client sends these on retry)

**Test:** Wire-format round-trip for IncompleteResult. Verify snake_case in JSON output.

## Phase 2: Server dispatch — detect inputResponses on retry

**Files:** `server/dispatch.go` or `server/mrtr.go` (new)

- [ ] In tools/call dispatch: if `params.inputResponses` is present, inject into `ToolContext`
- [ ] If `params.requestState` is present, verify HMAC (reuse existing infra), inject into context
- [ ] Tool handler can access `ctx.InputResponses()` to check what the client sent
- [ ] Tool handler can access `ctx.RequestState()` for any server-side state

**Test:** Handler receives inputResponses correctly on retry.

## Phase 3: Server handler API — returning IncompleteResult

**Files:** `core/tool.go`, `server/dispatch.go`

- [ ] Tool handler returns `IncompleteResult` as a special value (not error):
  ```go
  func myTool(ctx ToolContext, req ToolRequest) (ToolResult, error) {
      name := ctx.InputResponse("user_name")
      if name == nil {
          return ctx.RequestInput(map[string]InputRequest{
              "user_name": {Method: "elicitation/create", Params: ...},
          })
      }
      return TextResult("Hello, " + name), nil
  }
  ```
- [ ] `ctx.RequestInput(reqs)` returns a sentinel `ToolResult` with `IsIncomplete: true` + `InputRequests`
- [ ] Dispatch layer detects `IsIncomplete`, wraps as `IncompleteResult` with HMAC `requestState`
- [ ] `requestState` encodes: tool name, accumulated inputResponses keys answered, expiry

**Test:** Tool returns incomplete → client gets IncompleteResult on wire.

## Phase 4: Multi-round + composition

**Files:** `server/mrtr.go`

- [ ] Multi-round: handler can return IncompleteResult multiple times (round 1: ask name, round 2: ask confirmation)
- [ ] `requestState` carries which keys have been answered across rounds
- [ ] On each retry, dispatch merges new `inputResponses` with previously-answered keys from `requestState`
- [ ] MRTR → Tasks: if handler returns `CreateTaskResult` instead of `ToolResult`, dispatch emits task result

**Test:** Multi-round scenario + MRTR→task transition.

## Phase 5: Client

**Files:** `client/tools.go` or `client/mrtr.go` (new)

- [ ] `CallTool()` checks `result_type` on response:
  - `"complete"` or absent → return ToolResult (current behavior)
  - `"task"` → return CreateTaskResult (current behavior from SEP-2663)
  - `"incomplete"` → enter retry loop
- [ ] Retry loop:
  1. Parse `inputRequests` from IncompleteResult
  2. Call user-provided `InputHandler` callback to gather responses
  3. Re-send same `tools/call` with `inputResponses` + `requestState`
  4. Repeat until complete/task/error
- [ ] `InputHandler` interface: `func(reqs map[string]InputRequest) (map[string]json.RawMessage, error)`
- [ ] Default InputHandler for common cases (auto-elicit via client's Elicit, auto-sample via client's Sample)

**Test:** Mock server returning incomplete → client retries → gets complete.

## Phase 6: Example + conformance

**Files:** `examples/mrtr/`, `conformance/mrtr/`

Example server tools (matching conformance contract):
- `test_tool_with_elicitation` — asks for name, returns greeting
- `test_tool_with_sampling` — asks model to generate, returns result
- `test_tool_with_list_roots` — asks for roots, returns root list
- `test_tool_multi_input` — asks for multiple inputs in one round
- `test_tool_multi_round` — asks name (round 1), then confirmation (round 2)
- `test_tool_with_task` — MRTR gathers input, then returns CreateTaskResult

Conformance scenarios (matching upstream PR 188):
- [ ] Basic elicitation round-trip
- [ ] Basic sampling round-trip
- [ ] Basic list roots round-trip
- [ ] requestState echo across rounds
- [ ] Multiple inputRequests in one round
- [ ] Multi-round (incomplete → incomplete → complete)
- [ ] MRTR → task transition
- [ ] Missing inputResponse handling

## Implementation order

```
Phase 1 (types + rename) → Phase 2 (server detect) → Phase 3 (handler API) →
Phase 4 (multi-round) → Phase 5 (client) → Phase 6 (example + conformance)
```

## Open questions

1. **`result_type` rename** — breaking wire change for existing SEP-2663 consumers. Just rename since no external consumers yet.
2. **InputHandler callback shape** — sync callback is simplest for Option 1.
3. **requestState payload size** — if we serialize all answered inputResponses into requestState, large responses could bloat it. May need to store answers server-side and just carry a reference.

## Reference

- SEP-2322 spec: modelcontextprotocol/specification PR 2322
- Conformance tests: modelcontextprotocol/conformance PR 188
- Design review ticket: mcpkit 342
- Depends on: SEP-2663 (merged, issue mcpkit 320)
