# MRTR Tutorial — Multi Round-Trip Requests, end to end

Everything you need to know to write tools that gather input from the client cleanly across both the legacy and stateless MCP wires, plus a clear picture of where the older "server pushes a request to the client" mechanisms fit in (and where they're going).

> **Status.** Reflects the spec as of SEP-2322 (merged 2026-05-06), SEP-2575 (stateless wire), and SEP-2663 (tasks v2). mcpkit's reference fixtures live under [`examples/mrtr`](../examples/mrtr) and [`examples/tasks-v2`](../examples/tasks-v2); the conformance suite is in [panyam/mcpconformance](https://github.com/panyam/mcpconformance).

---

## 1. The core idea: a stateless continuation primitive

When a tool needs the client to do something — pick a file, confirm an action, sample a model, list its open workspaces — the spec gives the server a single mechanism: it returns an **`InputRequiredResult`** from `tools/call` describing what it needs, and the client re-invokes the same `tools/call` carrying the answer.

The mental model that breaks:

> "The server pauses the handler, holds the goroutine open, resumes when the client replies."

That's **not** what MRTR does. The handler returns immediately on every round. There is no goroutine waiting, no server-side pending state, no waiter channel.

The mental model that works:

> The server returns a **continuation token** (`requestState`). The client carries it forward. The server reconstructs context from the token on the next round, runs the same handler again with the accumulated answers, and either yields again or returns the final result.

State lives **in the token**, not in server memory. The handler is the same function on every round; it just sees a richer `ToolRequest.InputResponses` each time. This is what lets MRTR work identically on legacy session-based transports and on serverless stateless ones — the server can be a different Lambda invocation each round and the conversation still resumes.

If you've seen any of these elsewhere, the shape will feel familiar:

- **Algebraic effects / handlers** (Koka, Eff, Unison). The handler "performs an effect"; the client handles it and returns control. The cleanest analogy.
- **Generators / async iterators.** The handler "yields" an InputRequest and is resumed with the response. With a wrinkle: real generators preserve stack frames; MRTR replays from the top with accumulated state.
- **OAuth authorization-code flow.** Server says "I need consent, here's an opaque state token, come back with it"; client does its part; server validates state and resumes. The structural similarity is exact — mcpkit even uses HMAC-SHA256 on the token, same posture as a signed JWT continuation.
- **Continuation-passing style** at the protocol level. The `requestState` token IS the continuation — a serialized "where to resume" handle.

---

## 2. The wire shape

A tool call that needs input issues an `InputRequiredResult`:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "resultType": "input_required",
    "inputRequests": {
      "user_name": {
        "method": "elicitation/create",
        "params": {
          "message": "What is your name?",
          "requestedSchema": { "type": "object", ... }
        }
      }
    },
    "requestState": "eyJhbGciOiJIUzI1NiI...<signed token>"
  }
}
```

The client picks up the request, satisfies it (asks the user / samples the model / lists its roots), and re-invokes the **same** `tools/call` with `inputResponses` keyed by the same map keys:

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "tools/call",
  "params": {
    "name": "test_tool_with_elicitation",
    "arguments": {},
    "inputResponses": {
      "user_name": { "action": "accept", "content": { "name": "Alice" } }
    },
    "requestState": "eyJhbGciOiJIUzI1NiI..."
  }
}
```

A round can carry **multiple** input requests at once (the map is plural). All of them resolve in a single client round-trip. Likewise a handler can take **multiple rounds** — each round mints a fresh `requestState`, and the server forwards prior-round answers via the token so the handler sees the full accumulated state on the final round.

### Map keys are opaque

`"user_name"` is server-chosen and opaque to the client. The spec says clients MUST treat them as round-trip echo strings — never parse or interpret them. mcpkit picks readable names for debuggability; that's a server-side convention, not a wire contract.

### `requestState` is the continuation token

The server can sign it (HMAC) or run it as plaintext. mcpkit's signed mode encodes `{tool, accumulated answers, exp}` as the state; the verifier rejects:

- malformed tokens (`ErrRequestStateMalformed`),
- signature mismatches or **tool-name mismatches** ("token issued for tool A replayed against tool B" — `ErrRequestStateInvalidSignature`),
- expired tokens (`ErrRequestStateExpired`).

Production deployments should always set a signing key via `server.WithRequestStateSigning(...)`. Plaintext mode is for tests and demos.

---

## 3. What server-to-client methods does MRTR cover?

The `method` field on each `InputRequest` is one of the **methods the client offered** — i.e., the menu the client published in its capabilities. In practice, the three the spec scopes MRTR to are:

| Method | What the server is asking for | Client capability that enables it |
|---|---|---|
| `elicitation/create` | A user-facing prompt; the client renders, user answers | `elicitation: {}` |
| `sampling/createMessage` | A model completion the client routes to its LLM | `sampling: {}` |
| `roots/list` | The client's current set of workspace roots (URIs) | `roots: {}` |

The server doesn't get to invent new MRTR methods. It picks from what the client offered in `initialize` (legacy) or per-request `_meta.clientCapabilities` (stateless) — see §4.

### What's `roots/list` and why does it exist?

A **root** is a URI the client tells the server represents "where the user is currently working." Each root is a `{uri, name?}` pair — typically `file://`, `https://`, or some scheme-prefixed location.

Examples:

- An IDE host might expose `file:///Users/sri/projects/mcpkit/` when that project is open.
- A multi-project workspace might expose two roots: `file:///work/repo-a/`, `file:///work/repo-b/`.
- A browser-style host might expose `https://github.com/panyam/mcpkit` when the user has that repo focused.

**Why servers need it.** An MCP server has no innate idea what filesystem or workspace the user is in. The host (Claude Code, Claude Desktop, Cursor, …) does. Without `roots/list`, a server would have to either:

- ask the user via elicitation every time (annoying and the answer rarely changes),
- take filesystem paths as tool arguments (verbose and error-prone),
- or guess (broken).

Roots gives the server a structured, programmatic way to ask the client *"what is the user working on right now?"* and get back a list it can scope its operations against. It's the protocol-level answer to "current working directory" in a multi-host, sandboxed world.

**How it's used in practice.**

- A code-search server calls `roots/list` once, gets `[file:///Users/sri/projects/mcpkit/]`, and scopes all its searches to that path. No tool argument needed.
- A diagnostics server reads `roots/list` to know which file tree to type-check.
- A docs server scopes its lookup index to the project root.

**Why it's its own method instead of an elicitation.** Elicitation asks the user a free-form question; the answer is whatever the user types and is request-scoped. Roots are structured data the host already has — no user prompt needed — and they're stable across the session. Making it its own method lets clients return roots without user friction and lets hosts model "what's open" as first-class state.

A handler that needs the current roots issues:

```go
return ctx.RequestInput(core.InputRequests{
    "client_roots": core.InputRequest{
        Method: "roots/list",
        Params: json.RawMessage(`{}`),
    },
})
```

— see [`basicListRootsTool`](../examples/mrtr/main.go) (the A3 fixture).

---

## 4. Where the client publishes its capability menu — and how that changes per wire

The rule the spec enforces is universal:

> The server picks `InputRequest.method` from the menu of methods the client declared support for. If the client didn't declare `sampling`, the server can't legally ask for `sampling/createMessage`.

What changes between wires is **when and where** the menu is published.

| Wire | Menu source | Lifetime | Per-request override? |
|---|---|---|---|
| **Legacy** | `initialize` response | Session — cached server-side until session ends | Yes — `_meta.clientCapabilities` on a request overrides / augments the session cache (SEP-2575 also targets legacy) |
| **Stateless** | `_meta.clientCapabilities` on **every** request | Request — declared fresh each time, no server-side cache | Same field is the only source |

On stateless, the envelope is required on every request:

```json
{
  "params": {
    "name": "tools/call",
    "_meta": {
      "io.modelcontextprotocol/protocolVersion":    "DRAFT-2026-v1",
      "io.modelcontextprotocol/clientInfo":         { ... },
      "io.modelcontextprotocol/clientCapabilities": {
        "elicitation": {},
        "sampling":    {},
        "roots":       {},
        "extensions":  { "io.modelcontextprotocol/tasks": {} }
      }
    }
  }
}
```

A stateless request that omits `clientCapabilities` (or omits `_meta` entirely) is — from the server's perspective — a client that supports nothing. The server can't fall back to session state because there isn't any. The contract is *"if you want the server to ask you for X, declare X in `_meta` every time you might want to be asked."* This is the price stateless pays for being restart-safe.

### The mcpkit helper

[`core.ClientSupportsExtensionForRequest(ctx, key, perRequestCapsRaw)`](../core/stateless.go) transparently merges the session-cached caps (if any) with the per-request `_meta` override and gives you a single yes/no. Your handler or middleware doesn't need to special-case the wire. `taskV2Middleware` uses exactly this helper to gate the tasks extension; a future "MRTR capability gate" for elicitation/sampling/roots would use the same shape.

---

## 5. `progressToken` — who mints it and what it's for

**The client mints it.** It's a single-source-of-truth correlation tag: the client picks an opaque value (any JSON scalar — string, number, even null), attaches it to the outgoing request under `_meta.progressToken`, and uses that same value to match incoming `notifications/progress` events back to the request that asked for them.

```jsonc
// client → server
{
  "method": "tools/call",
  "params": {
    "name": "summarize",
    "arguments": { ... },
    "_meta": {
      "progressToken": "req-42"   // ← client's correlation tag
    }
  }
}

// server → client (later, on the push channel)
{
  "method": "notifications/progress",
  "params": {
    "progressToken": "req-42",    // ← server echoes the same token
    "progress": 47,
    "total": 100,
    "message": "Processing batch 47/100"
  }
}
```

The server never invents one. There'd be no client-side correlation map to look it up in.

Three layers in mcpkit:

1. **Client mints + sends.** The client library accepts a `ProgressToken any` field on its call options and serializes it as `_meta.progressToken`.
2. **Server reads + threads.** The dispatcher unmarshals the envelope, pulls `progressToken`, and constructs a `core.ToolContext` carrying it. Handlers access it via `ctx.ProgressToken()` or `req.ProgressToken`.
3. **Server emits with the same token.** `core.EmitProgress(ctx, token, current, total, message)` wraps a `notifications/progress` carrying the original token.

If the client didn't send one, the field stays `nil` and `EmitProgress` becomes a no-op (no addressee to correlate against). If you see a server-side fallback like:

```go
progressToken = tc.ProgressToken()
if progressToken == nil {
    progressToken = tc.TaskID()
}
```

— that's the app synthesizing a token because the client didn't, *as a convenience*. The protocol doesn't define this fallback; it's app-level behavior.

---

## 6. What `notifications/progress` and `notifications/message` were for — and what replaces them inside a task

Both are **server-to-client streaming notifications** on the persistent push channel:

- **`notifications/progress`** — progress updates for a single in-flight request the client opted into tracking. Use case: a 30-second compute job that wants to update an IDE progress bar.
- **`notifications/message`** — server-side log emission scoped by `logging/setLevel`. Use case: live operator logging, devtools output, audit trail.

SEP-2663's **G6 rule** says: **a task's notification channel is reserved for `notifications/tasks` (the lifecycle event stream) and MUST NOT carry `notifications/progress` or `notifications/message`.**

Three reasons the spec went this way:

1. **Wire homogeneity.** A task is observed via `tasks/get` polling or `notifications/tasks` SSE. Mixing in progress/message events on the same stream would force every task-aware client to disambiguate between "task lifecycle event" and "tool-internal status" — and they'd need to do that in a way that's consistent across servers. Cleaner: tasks emit task events, full stop.
2. **Stateless-wire feasibility.** Progress/message both assume a long-lived push channel. On the SEP-2575 stateless wire there isn't one. Tasks are how stateless servers expose long-running work; saying "tasks don't speak progress/message" lets stateless servers be fully spec-compliant for tasks without implementing a streaming back-channel they fundamentally can't have.
3. **No silent loss.** A handler emitting progress on legacy lands on the GET SSE stream; the same handler under stateless silently fails to deliver. Forbidding them everywhere makes the contract uniform.

### What replaces them

| Old | New (inside a task) | What it gets you |
|---|---|---|
| `notifications/progress` | `tc.SetStatus(...)` + `statusMessage` on `TaskInfo` | Per-task progress is observed via `tasks/get` (or `notifications/tasks` if the client is listening). Status transitions and the `statusMessage` field replace progress %. |
| `notifications/message` | Structured `result.content` when the task completes; for live observability, out-of-band server-side logging (your own log infra, OpenTelemetry, ...) | The task's `result` is where structured output goes. For live ops visibility, the spec is "use real logging" — MCP isn't the transport for that. |
| Either, during sync handler phase | **Still works.** The G6 filter is goroutine-scoped only — a sync handler returning a `core.ToolResult` (or running an MRTR round) can still call `EmitProgress` / `EmitLog` on the request ctx | Use this for short tool calls that don't need to be tasks |

### How mcpkit enforces it

[`ext/tasks/tasks.go`](../ext/tasks/tasks.go) wraps the continuation goroutine's `bgCtx` with a session-notify filter:

```go
bgCtx = core.ApplySessionNotifyFilter(bgCtx,
    "notifications/progress",
    "notifications/message",
)
```

So a handler written for the pre-G6 world doesn't *break* when run as a task — it just stops emitting those notifications. They no-op silently. That's what made the migration to GoAsync mechanical instead of a behavioral change.

The filter is **goroutine-scoped only**. A handler that returns sync (no GoAsync, no MRTR round) runs on the unfiltered POST ctx and can still emit. That's a deliberate narrowing — sync handlers on `TaskSupport=optional/required` tools are responsible for not leaking notifications they shouldn't.

---

## 7. When to use what — MRTR vs push vs task input flow

Three mechanisms for "server asks the client for something." Picking the right one matters.

| Mechanism | Wire | Trigger | Best for |
|---|---|---|---|
| **MRTR** (SEP-2322) — `InputRequiredResult` | Both legacy and stateless | During a single `tools/call` execution | One-shot prompts during a tool: confirm-then-do, gather an API key before running, etc. Each round is one HTTP cycle. |
| **Push** — server-initiated `sampling/createMessage` / `elicitation/create` / `roots/list` requests on the SSE push channel | **Legacy only** | Anytime — during a tool call, or out of band | Real-time interactions on the legacy wire. **On the stateless wire `ctx.Sample` / `ctx.Elicit` return `ErrNoRequestFunc` by construction** — there's no persistent push channel. |
| **Tasks input flow** (SEP-2663) — `tc.TaskElicit(...)` / `tc.TaskSample(...)` | Both wires (once stateless MRTR lands — see issue 452) | Inside a running task | Long-running tasks that need input mid-execution. The task parks in `input_required`; the client observes via `tasks/get` and resumes via `tasks/update`. State is scoped to the task lifetime, not to one MRTR round. |

### Decision flow

```
Is the server-to-client request happening inside a tool call?
├── No → push (legacy only — not supported on stateless)
└── Yes:
    ├── Is the tool registered with TaskSupport=optional/required AND running as a task?
    │   ├── Yes → tc.TaskElicit / tc.TaskSample (parks the task, resumes via tasks/update)
    │   └── No → MRTR (ctx.RequestInput returns InputRequiredResult)
    └── For the gather-then-go-async pattern (gather input fast via MRTR, then escalate to a task for the slow work), see §9.
```

### Where push is heading

Once SEP-2322 is widely negotiated, the push path is reachable by deprecation:

1. **Today.** `ctx.Sample` / `ctx.Elicit` work on legacy, error out on stateless. Use MRTR for new tool code that wants to work on both wires.
2. **Next.** Document MRTR as the recommended path; keep `ctx.Sample` / `ctx.Elicit` as legacy aliases that internally route through MRTR where possible.
3. **Eventually.** When tools' `requiredCapabilities` can opt into "MRTR-aware client only", the push path becomes dead code for sampling/elicitation/roots/list. **Notifications remain** on the push channel (lifecycle events, list-changed events) — those don't have an MRTR shape.

---

## 8. Writing handlers — the canonical state machine pattern

MRTR handlers are state machines on `InputResponses`. The same handler runs on every round; it branches on what's been answered so far.

### One-round example (basic elicitation)

```go
func basicElicitationTool(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
    resp := ctx.InputResponse("user_name")
    if resp == nil {
        // Round 1 — ask.
        return ctx.RequestInput(core.InputRequests{
            "user_name": core.InputRequest{
                Method: "elicitation/create",
                Params: json.RawMessage(`{"message":"What is your name?","requestedSchema":{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}}`),
            },
        })
    }
    // Round 2 — answer in hand, do the work.
    var er struct {
        Action  string `json:"action"`
        Content struct{ Name string `json:"name"` } `json:"content"`
    }
    if err := json.Unmarshal(resp, &er); err != nil {
        return core.ErrorResult("malformed elicitation response: " + err.Error()), nil
    }
    return core.TextResult(fmt.Sprintf("Hello, %s!", er.Content.Name)), nil
}
```

The full set of seven canonical patterns (sampling, roots, multi-input, multi-round, requestState round-trip, wrong-key tolerance) lives in [`examples/mrtr/main.go`](../examples/mrtr/main.go) and is exercised by the conformance scenarios.

### Multi-round example (accumulate state across rounds)

```go
func multiRoundTool(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
    if ctx.InputResponse("step1") == nil {
        return ctx.RequestInput(core.InputRequests{
            "step1": core.InputRequest{Method: "elicitation/create", Params: ...},
        })
    }
    if ctx.InputResponse("step2") == nil {
        return ctx.RequestInput(core.InputRequests{
            "step2": core.InputRequest{Method: "elicitation/create", Params: ...},
        })
    }
    // Both answered — server delivered prior-round answers via requestState.
    return core.TextResult(buildFinalAnswer(...)), nil
}
```

The dispatcher merges prior-round answers from `requestState` into `InputResponses` before invoking the handler — so the handler always sees the full accumulated map regardless of round count.

### Wrong-key tolerance

If the client sends an `inputResponses` key the server didn't emit, the handler's `InputResponse("user_name")` check returns `nil` and the handler re-requests. The conformance suite asserts this is the right behavior (vs erroring) — clients can race against state, and re-requesting is more user-friendly.

---

## 9. Composing MRTR with tasks — the GoAsync pattern (SEP-2663)

The killer composition: a single tool can run an MRTR round-trip to gather input *first*, then escalate to a background task for the slow work. This is what mcpkit issue 347 / PR 484 unblocked.

The pattern:

```go
func mrtrTaskCompositionTool(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
    // Phase 3: running inside the continuation goroutine (a task was minted).
    if tasks.GetTaskContext(ctx) != nil {
        // Do the heavy work; TaskContext gives us TaskElicit / SetStatus / etc.
        result := doExpensiveWork(ctx, req)
        return result, nil
    }

    // Phase 1: sync, no inputResponses yet — MRTR round.
    if ctx.InputResponse("user_name") == nil {
        return ctx.RequestInput(core.InputRequests{
            "user_name": core.InputRequest{Method: "elicitation/create", Params: ...},
        })
    }

    // Phase 2: sync, MRTR loop is done — escalate to async.
    return core.ToolResult{GoAsync: true}, nil
}
```

Three phases, one handler:

1. **Sync, no `user_name` yet** → return `InputRequiredResult`. `taskV2Middleware` sees an MRTR round; passes through unchanged; no task created.
2. **Sync, `user_name` present** → return `core.ToolResult{GoAsync: true}`. `taskV2Middleware` sees the sentinel; mints a task; spawns a continuation goroutine; returns `CreateTaskResult`.
3. **Goroutine, `TaskContext` attached** → real work runs here. The handler detects the `TaskContext` and branches into the async path. The result is stored on the task; the client retrieves via `tasks/get`.

### Spec separation that you can rely on (per SEP-2663)

- MRTR `requestState` does **not** carry into the task's `requestState` — the task gets its own per-task input state.
- Task `inputRequests` keys (if the goroutine later calls `TaskElicit`) are scoped to the task lifetime — distinct from MRTR phase keys.
- Clients don't need to deduplicate across the two flows.

mcpkit's [`mrtr-08`](https://github.com/panyam/mcpconformance) conformance scenario asserts all three.

### Why this needed a middleware refactor

Pre-Option-2, mcpkit's `taskV2Middleware` minted the task **before** the handler ran. That made the composition impossible: round 1 always emitted `CreateTaskResult` first, because the task was already minted before the handler could ever return `InputRequiredResult`. The 2026-05-19 decision on issue 347 inverted the order — handler runs synchronously first, middleware peeks at what came back, dispatches accordingly. See PR 484's "Decision log" for the alternative shapes considered (closure-carrying sentinel, always-goroutine for sync results, etc.) and why we landed on Option A strict.

---

## 10. Quick reference

### Tool handler shape

```go
import (
    "github.com/panyam/mcpkit/core"
    tasks "github.com/panyam/mcpkit/ext/tasks"
)

func handler(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
    // (Optional, when composing with tasks v2)
    if tasks.GetTaskContext(ctx) != nil {
        return doAsyncWork(ctx, req)
    }

    // MRTR loop — branch on what's been answered.
    if ctx.InputResponse("foo") == nil {
        return ctx.RequestInput(core.InputRequests{
            "foo": core.InputRequest{Method: "elicitation/create", Params: ...},
        })
    }

    // Everything gathered — either return sync result, or escalate to async.
    return core.ToolResult{GoAsync: true}, nil  // or do sync work and return
}
```

### Server setup

```go
srv := server.NewServer(info,
    server.WithRequestStateSigning([]byte(signingKey), 24*time.Hour),  // recommended
)
srv.RegisterTool(toolDef, handler)
tasks.Register(tasks.Config{Server: srv})  // if any tools opt into TaskSupport
```

### Client request shape

```jsonc
// Round 1
{
  "method": "tools/call",
  "params": {
    "name": "my_tool",
    "arguments": { ... },
    "_meta": {
      // Stateless requires the full envelope on every request.
      "io.modelcontextprotocol/protocolVersion":    "DRAFT-2026-v1",
      "io.modelcontextprotocol/clientInfo":         { ... },
      "io.modelcontextprotocol/clientCapabilities": {
        "elicitation": {},
        "extensions":  { "io.modelcontextprotocol/tasks": {} }
      },
      "progressToken": "req-42"  // optional, for notifications/progress correlation
    }
  }
}

// Round 2 (after server returned InputRequiredResult)
{
  "method": "tools/call",
  "params": {
    "name": "my_tool",
    "arguments": { ... },
    "_meta": { ... },                          // same envelope on every call
    "inputResponses": { "foo": { ... } },      // keyed by the server's emitted keys
    "requestState": "<token from round 1>"     // echo back unchanged
  }
}
```

### Errors to expect

| Error | When | Action |
|---|---|---|
| `-32602 invalid params: request params missing required _meta envelope` | Stateless request without `_meta` | Add the SEP-2575 `_meta` envelope. |
| `ErrRequestStateMalformed` / `Invalid signature` / `Expired` | `requestState` tampered, replayed across tools, or older than TTL | Restart the tool call from round 1. |
| `-32003 missing required client capability` | Server needs a capability (e.g., `tasks` extension) the client didn't declare | Update the client's `_meta.clientCapabilities` to declare it. |

---

## See also

- [`examples/mrtr/main.go`](../examples/mrtr/main.go) — eight canonical MRTR fixtures including the composition pattern (A8 / `test_tool_with_task`).
- [`examples/tasks-v2/main.go`](../examples/tasks-v2/main.go) — task fixtures (slow_compute, confirm_delete, multi_input, etc.) all using the GoAsync pattern.
- [`ext/tasks/README.md`](../ext/tasks/README.md) — task extension overview and handler-pattern reference.
- [`docs/TASKS_V2_MIGRATION.md`](TASKS_V2_MIGRATION.md) — v1 → v2 migration guide.
- [`docs/SEP_2663_TASKS_CONFORMANCE_PLAN.md`](SEP_2663_TASKS_CONFORMANCE_PLAN.md) — task conformance status.
- [panyam/mcpconformance](https://github.com/panyam/mcpconformance), branch `feat/tasks-mrtr-extension` — SEP-2322 + SEP-2663 conformance scenarios.
- Issue 452 — stateless MRTR support follow-up.
- Issue 485 — multi-tenant isolation for stateless task store follow-up.
