# MRTR — Multi Round-Trip Requests (SEP-2322)

How a `tools/call` pauses for input *without* holding the call open. Six questions.

> **Kind:** root *(FAQ-style)* · **Prerequisites:** [request-anatomy](./request-anatomy.md), [extension-mechanisms](./extension-mechanisms.md)
> **Reachable from:** [README](./README.md), [extension-mechanisms](./extension-mechanisms.md) Next-to-read, [request-anatomy](./request-anatomy.md) Next-to-read
> **Branches into:** [tasks](./tasks.md) *(stub, root)*
> **Spec:** SEP-2322 (landed) · **Code:** [`server/mrtr.go`](https://github.com/panyam/mcpkit/blob/main/server/mrtr.go) · [`client/mrtr.go`](https://github.com/panyam/mcpkit/blob/main/client/mrtr.go) · [`core/handler_context.go`](https://github.com/panyam/mcpkit/blob/main/core/handler_context.go) *(`RequestInput`, `NewToolContextWithMRTR`, `InputResponses` accessor)* · [`core/tool.go`](https://github.com/panyam/mcpkit/blob/main/core/tool.go) *(`ToolResult.IsInputRequired`)* · [`core/task_v2.go`](https://github.com/panyam/mcpkit/blob/main/core/task_v2.go) *(`InputRequiredResult`, `InputRequest`/`InputRequests`/`InputResponses`, `MRTRRoundState`, sign helpers)*

## Prerequisites

- You understand how a `tools/call` dispatches and what the handler context provides. → If not, read [request-anatomy](./request-anatomy.md).
- You know what `_meta` and method-namespace extensions are. → If not, read [extension-mechanisms](./extension-mechanisms.md).
- *(Helpful but not required)* You know the reverse-call pattern (sampling, elicitation, roots) — MRTR is the contrast. See [reverse-call](./reverse-call.md).

## Context

A tool needs information from the user mid-execution: a confirmation, a credential, an LLM completion. The natural shape is a *reverse call* — the server-side handler synchronously invokes `elicitation/create` (or `sampling/createMessage`, or `roots/list`) and waits for the response. That works, but it forces the server to keep the request alive in memory for as long as the user takes to type, and a transport drop while waiting strands the call.

**MRTR** (Multi Round-Trip Requests, [SEP-2322](#)) gives the server an alternative: instead of awaiting reverse calls, the handler returns `InputRequiredResult` with a list of *input requests* + an opaque `requestState` token. The client resolves the inputs, retries the same `tools/call` with `inputResponses` + the echoed token, and the server completes (or asks for another round). **The server keeps no per-round state** — the token is the round handle.

mcpkit's default client-side `InputHandler` routes MRTR's input requests through the *same* dispatch path as real reverse calls, so a host that already supports `elicitation/create` / `sampling/createMessage` / `roots/list` gets MRTR for free.

## Q1 — What problem does MRTR solve that reverse calls don't?

Reverse calls and MRTR can both deliver "server gets data from client mid-call." They make different tradeoffs.

| Concern | Reverse call (synchronous) | MRTR (round-trip) |
|---------|---------------------------|-------------------|
| **Server-side state during wait** | Handler is parked, holding goroutine + handler context + open transport channel | None. Handler returns; the request finishes. |
| **Transport drop while waiting** | Strands the call. Handler context dies; reverse-call response has nowhere to go. | Survives. Client reconnects, retries the same `tools/call` with the same `requestState` token. |
| **Latency for fast inputs** | Single round-trip per reverse call (request → response, in-process await) | Multi-round-trip (return InputRequiredResult, dispatch input methods on client, retry) |
| **Idempotent retries** | Tricky — the server has handler state | Trivial — client just retries with the same token |
| **Pause indefinitely** | Bad — server holds resources for the duration | Fine — token-bound, no server resources between rounds |
| **Mental model** | "Function call from inside the handler" | "Conversation: server says 'I need X', client provides X, retry" |

The decision rule: **if the handler can complete quickly given the inputs, reverse calls are simpler and lower-latency. If the wait might be long, the request can be detached, or the call needs to survive disconnects, MRTR is the right shape.** Tasks v2 (SEP-2663) leans on the same `InputRequiredResult` shape for the same reasons applied to long-running operations — see [Q6](#q6--composition-with-tasks-v2).

## Q2 — Worked example: a deploy tool that needs user confirmation

A `deploy_to_aws` tool needs two pieces of user input before it can proceed: a yes/no confirmation and an IAM role ARN. Two rounds total.

**Round 1 — initial call.** Client invokes the tool with the basic args:

```http
→ tools/call
{
  "jsonrpc": "2.0",
  "id": 7,
  "method": "tools/call",
  "params": {
    "name": "deploy_to_aws",
    "arguments": { "stack": "prod-api" }
  }
}
```

Server dispatches the handler. The handler decides it needs input, calls `ctx.RequestInput(...)` (see [Q3](#q3--server-side-returning-inputrequiredresult-from-a-handler)), and dispatch reshapes the response into an `InputRequiredResult`:

```http
← response
{
  "jsonrpc": "2.0",
  "id": 7,
  "result": {
    "resultType": "input_required",
    "inputRequests": {
      "confirm": {
        "method": "elicitation/create",
        "params": {
          "message": "Deploy stack 'prod-api' to AWS production?",
          "requestedSchema": {
            "type": "object",
            "properties": { "confirmed": { "type": "boolean" } },
            "required": ["confirmed"]
          }
        }
      },
      "role-arn": {
        "method": "elicitation/create",
        "params": {
          "message": "Provide the IAM role ARN to assume:",
          "requestedSchema": {
            "type": "object",
            "properties": { "arn": { "type": "string", "pattern": "^arn:aws:iam::" } },
            "required": ["arn"]
          }
        }
      }
    },
    "requestState": "<signed-token-1>"
  }
}
```

The keys (`"confirm"`, `"role-arn"`) are server-chosen identifiers. The client must echo them verbatim in the next round.

**Client resolves the inputs.** mcpkit's [`CallToolWithInputs`](https://github.com/panyam/mcpkit/blob/main/client/mrtr.go) sees `resultType: "input_required"` and calls the registered `InputHandler` (typically [`DefaultInputHandler`](https://github.com/panyam/mcpkit/blob/main/client/mrtr.go)) with the `InputRequests` map. The default handler iterates entries and routes each one through the same dispatcher the transport uses for *real* server-initiated requests — see [Q4](#q4--client-side-calltoolwithinputs-and-defaultinputhandler) for why this matters. The host's existing `elicitation/create` handler shows two forms; the user fills them in.

**Round 2 — retry with `inputResponses` + the echoed `requestState`:**

```http
→ tools/call
{
  "jsonrpc": "2.0",
  "id": 8,
  "method": "tools/call",
  "params": {
    "name": "deploy_to_aws",
    "arguments": { "stack": "prod-api" },
    "inputResponses": {
      "confirm":  { "confirmed": true },
      "role-arn": { "arn": "arn:aws:iam::123456789012:role/Deploy" }
    },
    "requestState": "<echoed-token-1>"
  }
}
```

Server verifies `requestState` (HMAC, expiry, tool-name match — see [Q5](#q5--requeststate-signing-contents-replay-defenses)), merges `inputResponses` into the accumulated answered map, dispatches the handler again. This time `ctx.HasInputResponses()` returns true, the handler reads `ctx.InputResponse("confirm")` / `ctx.InputResponse("role-arn")`, and runs the deploy:

```http
← response
{
  "jsonrpc": "2.0",
  "id": 8,
  "result": {
    "resultType": "complete",
    "content": [{ "type": "text", "text": "Deployed prod-api successfully" }]
  }
}
```

Two HTTP requests, two JSON-RPC `tools/call` invocations, two rounds. The server held no state between them — the second `tools/call` is a fresh dispatch that reconstructs the round context from the verified token.

**Three things to internalize from this example:**

- **Same method name, same arguments on retry.** The retry isn't "send the answers"; it's "do the call again, here are the answers I have now." A handler that's safely idempotent in `arguments` is naturally idempotent under MRTR retries.
- **Server-chosen keys round-trip verbatim.** `"confirm"` and `"role-arn"` are arbitrary tags chosen by the server. The client doesn't interpret them; it just echoes them paired with the answers.
- **`requestState` is opaque to the client.** It's a server-minted token; the client treats it as a string and echoes it. Tampering breaks the HMAC verification on round N+1.

## Q3 — Server side: returning InputRequiredResult from a handler

Handlers don't construct `InputRequiredResult` directly. They call `ctx.RequestInput(...)` and the dispatch layer reshapes the response.

```go
func deployHandler(ctx core.ToolContext, args DeployArgs) (core.ToolResult, error) {
    // First call has no inputResponses; we need confirmation + creds.
    if !ctx.HasInputResponses() {
        return ctx.RequestInput(core.InputRequests{
            "confirm": {
                Method: "elicitation/create",
                Params: mustMarshal(elicitationParams{
                    Message: fmt.Sprintf("Deploy stack %q to AWS production?", args.Stack),
                    RequestedSchema: confirmSchema,
                }),
            },
            "role-arn": {
                Method: "elicitation/create",
                Params: mustMarshal(elicitationParams{
                    Message:         "Provide the IAM role ARN to assume:",
                    RequestedSchema: arnSchema,
                }),
            },
        })
    }

    // Round 2+: read the answers and proceed.
    var confirm struct{ Confirmed bool }
    var creds struct{ ARN string }
    if err := ctx.InputResponseAs("confirm", &confirm); err != nil {
        return errResult(err), nil
    }
    if err := ctx.InputResponseAs("role-arn", &creds); err != nil {
        return errResult(err), nil
    }
    if !confirm.Confirmed {
        return textResult("Deploy cancelled by user."), nil
    }

    // Actually deploy with creds.ARN ...
    return textResult("Deployed " + args.Stack + " successfully"), nil
}
```

**What `ctx.RequestInput` does** ([`core/handler_context.go`](https://github.com/panyam/mcpkit/blob/main/core/handler_context.go)): builds a `ToolResult` with `IsInputRequired = true` and `InputRequests` populated. Returns it as a normal Go value. The flag is in-process plumbing only — never serialized.

**What dispatch does** when it sees `IsInputRequired = true`:

1. Calls [`mintRequestState`](https://github.com/panyam/mcpkit/blob/main/server/mrtr.go) on the server's `mrtrRuntime` — produces a fresh `requestState` token wrapping the accumulated `inputResponses` and the tool name. Signs with HMAC if a key is configured (see [Q5](#q5--requeststate-signing-contents-replay-defenses)).
2. Reshapes the `ToolResult` into an `InputRequiredResult` envelope with `resultType: "input_required"`, the `inputRequests`, and the freshly-minted `requestState`.
3. Returns to the transport.

**On retry**, dispatch goes the other direction:

1. Parses the `tools/call` request envelope ([`toolsCallEnvelope`](https://github.com/panyam/mcpkit/blob/main/server/mrtr.go)) — `inputResponses` and `requestState` live alongside `arguments` at the `params` top level, not nested under `arguments`.
2. Calls [`verifyRequestState`](https://github.com/panyam/mcpkit/blob/main/server/mrtr.go) — checks HMAC + TTL + tool-name match. Errors propagate as JSON-RPC errors (`ErrRequestStateInvalidSignature`, `ErrRequestStateExpired`, `ErrRequestStateMalformed`).
3. Calls [`mergeInputResponses`](https://github.com/panyam/mcpkit/blob/main/server/mrtr.go) — current round's responses overlay the previous-round responses encoded in the verified token. Current wins on key collision (so a client can correct an earlier answer by re-sending it under the same key).
4. Builds a new handler context with the merged `inputResponses` ([`NewToolContextWithMRTR`](https://github.com/panyam/mcpkit/blob/main/core/handler_context.go)) and dispatches the handler again. The handler sees `ctx.HasInputResponses() == true` and proceeds.

The handler is the **same function**, called with the **same arguments**, possibly multiple times, with progressively more answers in `ctx.InputResponses()`. Idempotence in `arguments` makes this safe.

> [!IMPORTANT]
> The handler doesn't see the `requestState` token directly (well, `ctx.RequestState()` exposes it for advanced cases). It also doesn't choose when to mint a new one. Dispatch handles the round bookkeeping; the handler just decides "do I have what I need? if not, ask for more."

## Q4 — Client side: CallToolWithInputs and DefaultInputHandler

[`CallToolWithInputs`](https://github.com/panyam/mcpkit/blob/main/client/mrtr.go) is the client-side retry loop. Pseudocode:

```
res ← Call("tools/call", { name, arguments })
while res.IsInputRequired():
    if rounds >= maxRounds: return ErrMRTRMaxRounds
    responses ← InputHandler(res.InputRequired.InputRequests)
    res ← Call("tools/call", { name, arguments, inputResponses, requestState: res.InputRequired.RequestState })
return res
```

Default `maxRounds` is **16**. Override with `WithMaxMRTRRounds(n)`. Hitting the cap returns `ErrMRTRMaxRounds` (wrappable via `errors.Is`) — almost always indicates a server-side bug (the handler keeps asking and never settles).

**`DefaultInputHandler(c)`** ([`client/mrtr.go`](https://github.com/panyam/mcpkit/blob/main/client/mrtr.go)) is the standard input resolver. It walks the `InputRequests` map and dispatches each entry through [`dispatchMRTRInputRequest`](https://github.com/panyam/mcpkit/blob/main/client/mrtr.go), which:

1. Synthesizes a `core.Request` with the input request's `method` and `params`.
2. Routes it through [`Client.HandleServerRequestWithContext`](https://github.com/panyam/mcpkit/blob/main/client/client.go) — **the same dispatcher the transport uses for real server-initiated requests**.
3. Reads the response and packages it as the corresponding `InputResponses` entry.

**This routing choice is the key value-add.** A host that has already wired up `samplingHandler`, `elicitationHandler`, `rootsHandler` for normal reverse calls gets MRTR support automatically — same code paths, same UI, same authorization gating, same middleware. URL-mode elicitation gating, host-approval flows, and any future client-side middleware all apply uniformly to both real reverse calls and MRTR-synthesized ones.

**Customizing the handler.** `DefaultInputHandler` is a starting point. Wrap or replace it for:

- **Custom input methods** — extensions can introduce new `InputRequest.Method` values; teach the handler to dispatch them.
- **Alternative routing** — a CLI client might route `elicitation/create` to a TUI form instead of the host's normal handler.
- **Test injection** — return canned responses without invoking the real elicitation UI; useful for end-to-end tests of MRTR-using tools.
- **Decline patterns** — a policy layer might decline certain input requests up-front (return an error from the handler, which propagates through `CallToolWithInputs` and aborts the loop).

## Q5 — requestState: signing, contents, replay defenses

`requestState` is an opaque token from the client's perspective. From the server's, it's the round handle.

**What's inside.** Server-side, dispatch builds a [`MRTRRoundState`](https://github.com/panyam/mcpkit/blob/main/core/task_v2.go):

```go
type MRTRRoundState struct {
    Tool     string                       // tool name; replay-prevention check
    Answered map[string]json.RawMessage   // accumulated inputResponses across rounds
    Exp      int64                        // unix expiry
}
```

The state is encoded one of two ways:

- **Signed** ([`SignMRTRState`](https://github.com/panyam/mcpkit/blob/main/core/task_v2.go)) — HMAC-SHA256 over the encoded payload using the configured key. Production deployments use this. Configure once via [`WithRequestStateSigning(key, ttl)`](https://github.com/panyam/mcpkit/blob/main/server/mrtr.go) — the same option also covers SEP-2663 task signing, so one HMAC config covers both surfaces.
- **Plaintext** ([`EncodeMRTRStatePlaintext`](https://github.com/panyam/mcpkit/blob/main/core/task_v2.go)) — base64url-encoded JSON, **no integrity guarantee**. Used only when no signing key is configured. The spec is explicit: servers MUST treat `requestState` as attacker-controlled, so plaintext mode is for development only.

**Three defenses the signed mode buys you:**

| Defense | Mechanism |
|---------|-----------|
| **Tampering** | HMAC mismatch → `ErrRequestStateInvalidSignature`. Attacker can't forge a token claiming arbitrary `Answered` data. |
| **Cross-tool replay** | `Tool` in the payload is checked on verify. A token issued for tool A replayed against tool B fails (`ErrRequestStateInvalidSignature`). |
| **Stale tokens** | `Exp` is embedded; verify checks expiry against the configured TTL (default 24h, override in `WithRequestStateSigning`). Token past expiry → `ErrRequestStateExpired`. |

**Plaintext mode still checks tool-name match.** Even without integrity, swapping tool names mid-round is a programmer error worth catching. So [`verifyRequestState`](https://github.com/panyam/mcpkit/blob/main/server/mrtr.go) checks `state.Tool == toolName` regardless of mode.

> [!CAUTION]
> Plaintext mode is `WithRequestStateSigning` *not configured*. The server still works, but `requestState` is a 128-bit random nonce with no integrity. Any client-fabricated token decodes successfully and the embedded answer map gets handed to the handler. **Do not run plaintext-mode MRTR in production.**

**Backward compatibility.** Earlier mcpkit shipped a single-round token shape that lived in the signed payload's `taskID` slot, prefixed with `"mrtr:"`. [`verifyRequestState`](https://github.com/panyam/mcpkit/blob/main/server/mrtr.go) has a fallback path that recognizes those tokens during the rollover so in-flight rounds aren't broken by deploys. Removable once no in-flight tokens predate the Phase 4 deploy.

## Q6 — Composition with tasks (v2)

Tasks v2 ([SEP-2663](https://modelcontextprotocol.io/specification/2025-06-18)) reuses the same `InputRequiredResult`-shaped pattern for long-running operations. A task can be in the `input_required` state with `inputRequests` and `requestState` populated; the client supplies inputs via the task's update path, and the task resumes.

Two consequences worth noting:

- **One signing key for MRTR.** `WithRequestStateSigning(key, ttl)` configures the HMAC for ephemeral MRTR (`tools/call` round-trips). SEP-2663 removed `requestState` from the tasks-v2 wire, so the v2 task surface no longer signs anything; the server-wide option is MRTR-only.
- **The `DefaultInputHandler` bridge applies to tasks too.** Whether the input request originated from a `tools/call` InputRequiredResult or a task in `input_required` state, the client-side handler dispatches it through the same Client-level dispatcher. The host doesn't write task-specific input handling.

The deeper task story — lifecycle, store, queue, detach/resume, the side-by-side v1+v2 registration pattern (the prior `RegisterTasksHybrid` was removed when v2 moved to `ext/tasks/`) — lives in [tasks](./tasks.md) *(stub)*.

> [!NOTE]
> **Mental model: MRTR is the wire mechanism, tasks is one place it gets used.** "Ephemeral" MRTR (the `tools/call` flow) and tasks v2 are different *surfaces* that share the same `InputRequiredResult` envelope, the same `InputRequest`/`InputResponses` types, and the same signing infrastructure. Read MRTR first; tasks builds on it.

## End-state (what downstream pages can assume)

After reading this page, downstream pages can assume:

- You know the **`InputRequiredResult` envelope** — `resultType: "input_required"`, the `inputRequests` map (server-chosen keys → method+params), the `requestState` token.
- You know the **retry shape** — same `tools/call` with `inputResponses` (matching keys) + echoed `requestState`. The server keeps no state between rounds.
- You know **`ctx.RequestInput(reqs)`** is the handler-side primitive, and dispatch reshapes the result into an `InputRequiredResult` envelope. The handler is the same function called with the same arguments; idempotence in `arguments` is the prerequisite.
- You know **`CallToolWithInputs`** runs the loop client-side, capped at `maxRounds` (default 16), and **`DefaultInputHandler`** routes input requests through the same dispatcher real reverse calls use — so the host's existing sampling/elicitation/roots handlers serve MRTR for free.
- You know **signed `requestState`** (HMAC-SHA256, TTL-bounded, tool-name-pinned) is the production mode; plaintext mode is dev-only with no integrity.
- You know **MRTR vs reverse calls** is a tradeoff between server-side state (RC: held; MRTR: none), transport-drop survival (RC: lost; MRTR: survives via token), and latency (RC: lower; MRTR: extra round-trips). The two end up at the same client-side handlers when `DefaultInputHandler` is used; the difference is *server-side*.
- You know **tasks v2 reuses the same envelope and signing**; the deep task story is its own page.

## Next to read

- **[Tasks](./tasks.md)** *(stub, root)* — long-running operations; tasks in `input_required` state use the same `InputRequiredResult`-shaped pattern. The deeper task lifecycle, store, queue, and detach/resume story.
- **[Reverse-call mechanics](./reverse-call.md)** — the synchronous-during-handler alternative; understanding the contrast clarifies when to use which.
- **[Cancellation deep-dive](./cancellation.md)** *(stub, leaf)* — how cancellation interacts with mid-MRTR-round state (the call across rounds is *not* one in-flight call from the cancel-id perspective, since each round is its own JSON-RPC request id).
