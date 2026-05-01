# MCP MRTR (SEP-2322) — Ephemeral IncompleteResult Round-Trips

Walks through the SEP-2322 ephemeral Multi Round-Trip Requests flow. The server returns `IncompleteResult{inputRequests, requestState}` when it needs more input from the client; the client resolves each `inputRequest` (elicitation, sampling, roots) locally and retries the SAME `tools/call` with `inputResponses` + the echoed `requestState`. Stateless on the server side — accumulated answers live inside `requestState` across rounds.

## What you'll learn

- **Connect to the MRTR server with capability handlers** — `client.WithElicitationHandler` / `WithSamplingHandler` / `WithRootsHandler` register the client-side callbacks. The walkthrough returns canned answers so the loop runs end-to-end without user interaction; in production these would prompt the user, hit an LLM, or read filesystem roots.
- **Round 1 (raw): tools/call → IncompleteResult** — Bypass the auto-loop helper to see the raw IncompleteResult shape. The discriminator is `result_type` (snake_case — the only MCP wire field that isn't camelCase). `inputRequests` is keyed by server-chosen opaque ids the client must echo verbatim.
- **Auto-loop: CallToolWithInputs runs the round-trip** — `client.CallToolWithInputs(ctx, c, name, args, handler)` collapses the whole loop. `DefaultInputHandler` synthesizes a server-to-client request for each `inputRequest` and routes it through `client.HandleServerRequestWithContext` — single source of truth for how the client responds to MCP method requests, whether they arrived over the back-channel or inlined inside an IncompleteResult.
- **Multi-round: server accumulates answers across rounds via requestState** — The wire only ships the LATEST round's `inputResponses`. Dispatch decodes prior answers from `requestState` (a signed `MRTRRoundState` containing the accumulated answers map), merges with the current round, and surfaces a unified map to the handler. Handlers stay stateless across rounds. The canned elicitation handler returns the same `name: Alice` for both prompts in this demo, hence the funny output — a real handler would branch on the elicitation message.

## Flow

```mermaid
sequenceDiagram
    participant Host as MCP Host (this client)
    participant Server as MCP Server (make serve)

    Note over Host,Server: Step 1: Connect to the MRTR server with capability handlers
    Host->>Server: POST /mcp — initialize (capabilities: elicitation, sampling, roots)
    Server-->>Host: serverInfo + capabilities

    Note over Host,Server: Step 2: Round 1 (raw): tools/call → IncompleteResult
    Host->>Server: tools/call: test_tool_with_elicitation {}
    Server-->>Host: { result_type: "incomplete", inputRequests: {user_name: {method: "elicitation/create", ...}}, requestState: "<token>" }

    Note over Host,Server: Step 3: Auto-loop: CallToolWithInputs runs the round-trip
    Host->>Server: tools/call: test_tool_with_elicitation
    Server-->>Host: IncompleteResult{user_name elicitation}
    Host->>Host: DefaultInputHandler → c.elicitationHandler → ElicitationResult{name: Alice}
    Host->>Server: tools/call (retry): {arguments: {}, inputResponses: {user_name: <result>}, requestState: <echo>}
    Server-->>Host: ToolResult: "Hello, Alice!"

    Note over Host,Server: Step 4: Multi-round: server accumulates answers across rounds via requestState
    Host->>Server: tools/call: test_incomplete_result_multi_round
    Server-->>Host: Round 1 IncompleteResult: ask step1 (name)
    Host->>Server: retry with inputResponses{step1}
    Server-->>Host: Round 2 IncompleteResult: ask step2 (color) — requestState now carries step1's answer
    Host->>Server: retry with inputResponses{step2} (NOT step1 — that's already in requestState)
    Server-->>Host: Round 3 ToolResult: "Hi Alice, your favorite color is Alice."
```

## Steps

### Setup

Start the MCP server in a separate terminal first:

```
Terminal 1:  make serve         # MRTR demo server on :8080
Terminal 2:  make demo          # this walkthrough (--tui for the interactive TUI)
```

### What MRTR adds to tools/call

v1 `tools/call` had two terminal shapes — a sync `ToolResult` or (with SEP-2663 Tasks) a `CreateTaskResult`. SEP-2322 adds a third **transient** shape:

- **`result_type: "complete"`** (or absent) — sync ToolResult, the call is done.
- **`result_type: "task"`** — server elected to spin off a task; client polls via `tasks/get` (SEP-2663).
- **`result_type: "incomplete"`** — server needs more input. The response carries `inputRequests` (a map of opaque keys → `{method, params}`) and an opaque `requestState`. The client resolves each input request locally, then RETRIES the same `tools/call` with the original arguments PLUS `inputResponses` (keyed by the same opaque ids) AND the echoed `requestState`.

The `inputRequests` methods are real MCP method names (`elicitation/create`, `sampling/createMessage`, `roots/list`). The client routes each through the same dispatcher it uses for real server-initiated requests — `client.HandleServerRequestWithContext` — so your existing `WithElicitationHandler` / `WithSamplingHandler` / `WithRootsHandler` callbacks just work.

`client.CallToolWithInputs(ctx, c, name, args, handler)` runs the loop automatically; `client.DefaultInputHandler(c)` is the standard handler that delegates to the client's capability callbacks.

### Step 1: Connect to the MRTR server with capability handlers

`client.WithElicitationHandler` / `WithSamplingHandler` / `WithRootsHandler` register the client-side callbacks. The walkthrough returns canned answers so the loop runs end-to-end without user interaction; in production these would prompt the user, hit an LLM, or read filesystem roots.

### Step 2: Round 1 (raw): tools/call → IncompleteResult

Bypass the auto-loop helper to see the raw IncompleteResult shape. The discriminator is `result_type` (snake_case — the only MCP wire field that isn't camelCase). `inputRequests` is keyed by server-chosen opaque ids the client must echo verbatim.

### Step 3: Auto-loop: CallToolWithInputs runs the round-trip

`client.CallToolWithInputs(ctx, c, name, args, handler)` collapses the whole loop. `DefaultInputHandler` synthesizes a server-to-client request for each `inputRequest` and routes it through `client.HandleServerRequestWithContext` — single source of truth for how the client responds to MCP method requests, whether they arrived over the back-channel or inlined inside an IncompleteResult.

### Step 4: Multi-round: server accumulates answers across rounds via requestState

The wire only ships the LATEST round's `inputResponses`. Dispatch decodes prior answers from `requestState` (a signed `MRTRRoundState` containing the accumulated answers map), merges with the current round, and surfaces a unified map to the handler. Handlers stay stateless across rounds. The canned elicitation handler returns the same `name: Alice` for both prompts in this demo, hence the funny output — a real handler would branch on the elicitation message.

### Where to look in the code

- Server dispatch: `server/dispatch.go` (handleToolsCall reshapes Incomplete into the wire envelope; merges accumulated answers from `requestState`)
- Server runtime: `server/mrtr.go` (`mrtrRuntime` — sign / verify / mint requestState tokens; `WithRequestStateSigning(key, ttl)` shared with SEP-2663 Tasks)
- Wire types: `core.IncompleteResult` / `MRTRRoundState` / `Sign|VerifyMRTRState` — core/task_v2.go
- Tool handler API: `ctx.RequestInput(reqs)` sentinel + `ctx.InputResponse(key)` / `HasInputResponses()` / `RequestState()` accessors — core/handler_context.go
- Client auto-loop: `client.CallToolWithInputs` + `DefaultInputHandler` — client/mrtr.go
- Client dispatch unification: `client.HandleServerRequestWithContext` — single switch for both real server-initiated requests AND MRTR-synthesized ones — client/client.go
- Conformance: `conformance/mrtr/scenarios.test.ts` (7 scenarios + 1 skipped composition; `make testconf-mrtr`)
- SEP-2322 spec: https://github.com/modelcontextprotocol/specification/pull/2322

## Run it

```bash
go run ./examples/mrtr/
```

Pass `--non-interactive` to skip pauses:

```bash
go run ./examples/mrtr/ --non-interactive
```
