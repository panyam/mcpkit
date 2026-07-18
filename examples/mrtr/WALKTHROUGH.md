# MCP MRTR (SEP-2322) — Ephemeral InputRequiredResult Round-Trips

Walks through the SEP-2322 ephemeral Multi Round-Trip Requests flow. The server returns `InputRequiredResult{inputRequests, requestState}` when it needs more input from the client; the client resolves each `inputRequest` (elicitation, sampling, roots) locally and retries the SAME `tools/call` with `inputResponses` + the echoed `requestState`. Stateless on the server side — accumulated answers live inside `requestState` across rounds. Renamed from `IncompleteResult` in SEP-2322 commit de6d76fb (merged 2026-05-06).

## What you'll learn

- **Connect to the MRTR server with capability handlers** — `client.WithElicitationHandler` / `WithSamplingHandler` / `WithRootsHandler` register the client-side callbacks. The walkthrough returns canned answers so the loop runs end-to-end without user interaction; in production these would prompt the user, hit an LLM, or read filesystem roots.
- **Round 1 (raw): tools/call → InputRequiredResult** — Bypass the auto-loop helper to see the raw InputRequiredResult shape. The discriminator is `resultType` — camelCase like every other MCP wire field. `inputRequests` is keyed by server-chosen opaque ids the client must echo verbatim. SEP-2322 commit de6d76fb (merged 2026-05-06) renamed this variant from IncompleteResult / `"incomplete"`.
- **Auto-loop: CallToolWithInputs runs the round-trip** — `client.CallToolWithInputs(ctx, c, name, args, handler)` collapses the whole loop. `DefaultInputHandler` synthesizes a server-to-client request for each `inputRequest` and routes it through `client.HandleServerRequestWithContext` — single source of truth for how the client responds to MCP method requests, whether they arrived over the back-channel or inlined inside an InputRequiredResult.
- **Multi-round: server accumulates answers across rounds via requestState** — The wire only ships the LATEST round's `inputResponses`. Dispatch decodes prior answers from `requestState` (a signed `MRTRRoundState` containing the accumulated answers map), merges with the current round, and surfaces a unified map to the handler. Handlers stay stateless across rounds. The canned elicitation handler returns the same `name: Alice` for both prompts in this demo, hence the funny output — a real handler would branch on the elicitation message.
- **Tracing: rounds 2+ link back to round 1 (SEP-414 P6)** — Without this PR the same operation produced N unrelated traces — operators looking at round N had no way to navigate to round 1. The vendor-namespaced `_meta.io.modelcontextprotocol/tracelink` field is mcpkit-specific today; upstream WG standardization of a bare cross-SDK name is a future-discussion item.

## Flow

```mermaid
sequenceDiagram
    participant Host as MCP Host (this client)
    participant Server as MCP Server (just serve)

    Note over Host,Server: Step 1: Connect to the MRTR server with capability handlers
    Host->>Server: POST /mcp — initialize (capabilities: elicitation, sampling, roots)
    Server-->>Host: serverInfo + capabilities

    Note over Host,Server: Step 2: Round 1 (raw): tools/call → InputRequiredResult
    Host->>Server: tools/call: test_tool_with_elicitation {}
    Server-->>Host: { resultType: "input_required", inputRequests: {user_name: {method: "elicitation/create", ...}}, requestState: "<token>" }

    Note over Host,Server: Step 3: Auto-loop: CallToolWithInputs runs the round-trip
    Host->>Server: tools/call: test_tool_with_elicitation
    Server-->>Host: InputRequiredResult{user_name elicitation}
    Host->>Host: DefaultInputHandler → c.elicitationHandler → ElicitationResult{name: Alice}
    Host->>Server: tools/call (retry): {arguments: {}, inputResponses: {user_name: <result>}, requestState: <echo>}
    Server-->>Host: ToolResult: "Hello, Alice!"

    Note over Host,Server: Step 4: Multi-round: server accumulates answers across rounds via requestState
    Host->>Server: tools/call: test_incomplete_result_multi_round
    Server-->>Host: Round 1 InputRequiredResult: ask step1 (name)
    Host->>Server: retry with inputResponses{step1}
    Server-->>Host: Round 2 InputRequiredResult: ask step2 (color) — requestState now carries step1's answer
    Host->>Server: retry with inputResponses{step2} (NOT step1 — that's already in requestState)
    Server-->>Host: Round 3 ToolResult: "Hi Alice, your favorite color is Alice."

    Note over Host,Server: Step 5: Tracing: rounds 2+ link back to round 1 (SEP-414 P6)
```

## Steps

### Setup

Start the MCP server in a separate terminal first:

```
Terminal 1:  just serve         # MRTR demo server on :8080
Terminal 2:  just demo          # this walkthrough (--tui for the interactive TUI)
```

### What MRTR adds to tools/call

v1 `tools/call` had two terminal shapes — a sync `ToolResult` or (with SEP-2663 Tasks) a `CreateTaskResult`. SEP-2322 adds a third **transient** shape:

- **`resultType: "complete"`** (or absent) — sync ToolResult, the call is done.
- **`resultType: "task"`** — server elected to spin off a task; client polls via `tasks/get` (SEP-2663).
- **`resultType: "input_required"`** — server needs more input. The response carries `inputRequests` (a map of opaque keys → `{method, params}`) and an opaque `requestState`. The client resolves each input request locally, then RETRIES the same `tools/call` with the original arguments PLUS `inputResponses` (keyed by the same opaque ids) AND the echoed `requestState`. Renamed from `"incomplete"` in SEP-2322 commit de6d76fb (merged 2026-05-06).

The `inputRequests` methods are real MCP method names (`elicitation/create`, `sampling/createMessage`, `roots/list`). The client routes each through the same dispatcher it uses for real server-initiated requests — `client.HandleServerRequestWithContext` — so your existing `WithElicitationHandler` / `WithSamplingHandler` / `WithRootsHandler` callbacks just work.

`client.CallToolWithInputs(ctx, c, name, args, handler)` runs the loop automatically; `client.DefaultInputHandler(c)` is the standard handler that delegates to the client's capability callbacks.

### Step 1: Connect to the MRTR server with capability handlers

`client.WithElicitationHandler` / `WithSamplingHandler` / `WithRootsHandler` register the client-side callbacks. The walkthrough returns canned answers so the loop runs end-to-end without user interaction; in production these would prompt the user, hit an LLM, or read filesystem roots.

#### Reproduce on the wire

```bash
# Initialize a session declaring the capabilities the server's inputRequests
# will exercise (elicitation, sampling, roots), then capture the session id.
SID=$(curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":"i","method":"initialize","params":{"protocolVersion":"2025-11-25","clientInfo":{"name":"x","version":"1"},"capabilities":{"elicitation":{},"sampling":{},"roots":{}}}}' \
  -D - -o /dev/null | grep -i 'mcp-session-id' | awk '{print $2}' | tr -d '\r\n')
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized"}' >/dev/null
echo "SID=$SID"
```

### Step 2: Round 1 (raw): tools/call → InputRequiredResult

Bypass the auto-loop helper to see the raw InputRequiredResult shape. The discriminator is `resultType` — camelCase like every other MCP wire field. `inputRequests` is keyed by server-chosen opaque ids the client must echo verbatim. SEP-2322 commit de6d76fb (merged 2026-05-06) renamed this variant from IncompleteResult / `"incomplete"`.

#### Reproduce on the wire

```bash
# A bare tools/call. The server needs input, so result comes back with
# resultType:"input_required", an inputRequests map (opaque key -> {method,
# params}), and an opaque requestState you echo verbatim on retry.
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"test_tool_with_elicitation","arguments":{}}}' \
  | jq '.result'
```

### Step 3: Auto-loop: CallToolWithInputs runs the round-trip

`client.CallToolWithInputs(ctx, c, name, args, handler)` collapses the whole loop. `DefaultInputHandler` synthesizes a server-to-client request for each `inputRequest` and routes it through `client.HandleServerRequestWithContext` — single source of truth for how the client responds to MCP method requests, whether they arrived over the back-channel or inlined inside an InputRequiredResult.

#### Reproduce on the wire

```bash
# Round 1: tools/call returns input_required + requestState.
R1=$(curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"test_tool_with_elicitation","arguments":{}}}')
STATE=$(echo "$R1" | jq -r '.result.requestState')

# Resolve the elicitation locally (canned {name: Alice} here), then RETRY the
# same tools/call with inputResponses (keyed by the opaque id round 1 returned,
# "user_name") PLUS the echoed requestState.
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"test_tool_with_elicitation\",\"arguments\":{},\"inputResponses\":{\"user_name\":{\"action\":\"accept\",\"content\":{\"name\":\"Alice\"}}},\"requestState\":\"$STATE\"}}" \
  | jq '.result'
```

### Step 4: Multi-round: server accumulates answers across rounds via requestState

The wire only ships the LATEST round's `inputResponses`. Dispatch decodes prior answers from `requestState` (a signed `MRTRRoundState` containing the accumulated answers map), merges with the current round, and surfaces a unified map to the handler. Handlers stay stateless across rounds. The canned elicitation handler returns the same `name: Alice` for both prompts in this demo, hence the funny output — a real handler would branch on the elicitation message.

#### Reproduce on the wire

```bash
# Round 1: server asks step1 (name).
R1=$(curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"test_incomplete_result_multi_round","arguments":{}}}')
S1=$(echo "$R1" | jq -r '.result.requestState')

# Round 2: retry with step1's answer. Server asks step2 (color); the new
# requestState now also encodes step1's answer.
R2=$(curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"test_incomplete_result_multi_round\",\"arguments\":{},\"inputResponses\":{\"step1\":{\"action\":\"accept\",\"content\":{\"name\":\"Alice\"}}},\"requestState\":\"$S1\"}}")
S2=$(echo "$R2" | jq -r '.result.requestState')

# Round 3: retry with ONLY step2 — step1 already rides inside requestState.
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"tools/call\",\"params\":{\"name\":\"test_incomplete_result_multi_round\",\"arguments\":{},\"inputResponses\":{\"step2\":{\"action\":\"accept\",\"content\":{\"color\":\"Alice\"}}},\"requestState\":\"$S2\"}}" \
  | jq '.result'
```

### Step 5: Tracing: rounds 2+ link back to round 1 (SEP-414 P6)

Without this PR the same operation produced N unrelated traces — operators looking at round N had no way to navigate to round 1. The vendor-namespaced `_meta.io.modelcontextprotocol/tracelink` field is mcpkit-specific today; upstream WG standardization of a bare cross-SDK name is a future-discussion item.

### Where to look in the code

- Server dispatch: `server/dispatch.go` (handleToolsCall reshapes InputRequired into the wire envelope; merges accumulated answers from `requestState`)
- Server runtime: `server/mrtr.go` (`mrtrRuntime` — sign / verify / mint requestState tokens; `WithRequestStateSigning(key, ttl)` shared with SEP-2663 Tasks)
- Wire types: `core.InputRequiredResult` / `MRTRRoundState` / `Sign|VerifyMRTRState` — core/task_v2.go
- Tool handler API: `ctx.RequestInput(reqs)` sentinel + `ctx.InputResponse(key)` / `HasInputResponses()` / `RequestState()` accessors — core/handler_context.go
- Client auto-loop: `client.CallToolWithInputs` + `DefaultInputHandler` — client/mrtr.go
- Client dispatch unification: `client.HandleServerRequestWithContext` — single switch for both real server-initiated requests AND MRTR-synthesized ones — client/client.go
- Conformance: panyam/mcpconformance fork (`src/scenarios/server/mrtr/`, 7 checks + 1 SKIPPED composition; upstream Draft PR modelcontextprotocol/conformance#262; `just testconf-mrtr` runs it)
- SEP-2322 spec: https://github.com/modelcontextprotocol/specification/pull/2322

## Run it

```bash
go run ./examples/mrtr/
```

Pass `--non-interactive` to skip pauses:

```bash
go run ./examples/mrtr/ --non-interactive
```
