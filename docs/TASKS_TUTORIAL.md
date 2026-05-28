# Tasks Tutorial — SEP-2663 server-directed async execution, end to end

Everything you need to know to write tools that run as long-lived background tasks, gather input mid-execution, surface progress, and compose cleanly with the MRTR (SEP-2322) sync round-trip pattern.

> **Status.** Reflects SEP-2663 (merged Final 2026-05-15), SEP-2575 (stateless wire), and SEP-2322 (MRTR — see [`docs/MRTR_TUTORIAL.md`](MRTR_TUTORIAL.md)).
>
> mcpkit's reference fixtures live under [`examples/tasks-v2`](../examples/tasks-v2) and [`examples/mrtr`](../examples/mrtr); the conformance suite is in [panyam/mcpconformance](https://github.com/panyam/mcpconformance), branch `feat/tasks-mrtr-extension`. The migration guide for v1 → v2 is at [`docs/TASKS_V2_MIGRATION.md`](TASKS_V2_MIGRATION.md).

---

## 1. The core idea: server-directed async execution

A **task** is a long-running tool invocation that gets a stable identifier and outlives any single HTTP round-trip. The client doesn't ask for one — the *server* decides whether a given `tools/call` should run as a task, based on a single piece of static metadata on the tool definition (`Execution.TaskSupport`) plus what the handler returns at runtime.

The decision flow on the server is intentionally simple:

```
tools/call arrives
       ↓
Look up tool's Execution.TaskSupport:
  ├── forbidden / absent → run sync, return ToolResult, never a task
  ├── optional → server may create a task (gated on client extension)
  └── required → server must create a task (gated on client extension)
       ↓
Has the client negotiated the tasks extension?
  ├── No, and TaskSupport=required → -32003 with requiredCapabilities
  ├── No, and TaskSupport=optional → run sync (no task)
  └── Yes → run the handler, then peek at what it returned
       ↓
Handler returned what?
  ├── core.InputRequiredResult (MRTR) → return as-is, no task created
  ├── core.ToolResult{GoAsync: true} → mint task, spawn continuation
  └── core.ToolResult (no GoAsync) → mint born-terminal task
```

This is the heart of SEP-2663's *"server decides"* posture: the v1 `task` hint in the client request is gone. The server runs the handler, sees what it produced, and decides whether to wrap it in task envelopes.

### When to use a task vs. sync vs. MRTR

| Pattern | Picks this when ... | Wire shape returned |
|---|---|---|
| **Sync ToolResult** | Tool does its work in milliseconds, no input needed mid-flight, no need to outlive the HTTP request | `ToolResult` |
| **MRTR rounds** | Tool needs *one or more pieces of input* up front to decide what to do, but doesn't need to keep state past the answer | `InputRequiredResult` then `ToolResult` |
| **Born-terminal task** | Tool was declared `TaskSupport=optional/required` for wire consistency, but a particular invocation happens to finish synchronously (cache hit, instant compute, etc.) | `CreateTaskResult` with `status: completed` |
| **GoAsync task** | Tool's real work is long, needs the goroutine, can call `TaskElicit`/`TaskSample`, emits progress, may need cancellation | `CreateTaskResult` + later `tasks/get` polling |

The killer feature — and the focus of §7 — is that **MRTR rounds and GoAsync tasks compose**: a single tool can do MRTR rounds for upfront input, then escalate to a task for the long work, then call `TaskElicit` mid-task if more input becomes necessary.

---

## 2. Wire shape

Tasks have their own three-method API alongside `tools/call`:

| Method | Direction | Purpose |
|---|---|---|
| `tools/call` | C → S | Tool invocation; server may respond with `CreateTaskResult` |
| `tasks/get` | C → S | Poll a task's state and (when terminal) inlined result |
| `tasks/update` | C → S | Deliver an `inputResponses` payload to a task parked in `input_required` |
| `tasks/cancel` | C → S | Request cancellation; goroutine sees `ctx.Done()` |
| `notifications/tasks` | S → C | Lifecycle status events; carries the same `DetailedTask` shape |

### `CreateTaskResult` (returned from `tools/call`)

The flat intersection of `Result` and `Task` (per SEP-2663):

```json
{
  "resultType": "task",
  "taskId": "task-a3f9...",
  "status": "working",
  "createdAt": "2026-05-28T12:00:00Z",
  "lastUpdatedAt": "2026-05-28T12:00:00Z",
  "ttlMs": 300000,
  "pollIntervalMs": 1000
}
```

MUST NOT carry `result`, `error`, `inputRequests`, or `requestState` — those live on `DetailedTask` returned by `tasks/get`.

### `DetailedTask` (returned from `tasks/get` and on `notifications/tasks`)

```json
{
  "taskId": "task-a3f9...",
  "status": "input_required",
  "createdAt": "...",
  "lastUpdatedAt": "...",
  "inputRequests": {
    "elicit-1": {
      "method": "elicitation/create",
      "params": { ... }
    }
  },
  "result": null,
  "error": null
}
```

When `status` is terminal:

- `completed` → `result` is the final `ToolResult` (may carry `isError: true` for tool-level errors)
- `failed` → `error` is a JSON-RPC-shaped `{code, message}` for protocol-level failures
- `cancelled` → both `result` and `error` are absent; `statusMessage` may carry detail

### `tasks/update`

The client delivers responses to in-task input requests:

```json
{
  "method": "tasks/update",
  "params": {
    "taskId": "task-a3f9...",
    "inputResponses": {
      "elicit-1": {
        "action": "accept",
        "content": { "confirm": true }
      }
    }
  }
}
```

The server's response is an empty ack (`{}`); the actual continuation happens out-of-band on the parked goroutine.

---

## 3. Server-directed task creation

### Declaring task eligibility

A tool opts into task execution by setting `Execution.TaskSupport`:

```go
srv.RegisterTool(
    core.ToolDef{
        Name:        "slow_compute",
        Description: "...",
        InputSchema: map[string]any{"type": "object"},
        Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportOptional},
    },
    handler,
)
```

Three values:

- **`TaskSupportForbidden`** (or absent `Execution`) — tool never runs as a task. Handler returns sync. Server ignores `GoAsync` if the handler somehow sets it.
- **`TaskSupportOptional`** — tool *may* run as a task, depending on what the handler does. If the client hasn't negotiated the tasks extension, the server falls back to sync.
- **`TaskSupportRequired`** — tool *must* run as a task. If the client hasn't negotiated the tasks extension, the server returns `-32003` (Missing Required Client Capability) with a structured `requiredCapabilities` payload so the client knows what to add.

### Client negotiation

The client must declare the extension. Two ways:

- **Legacy wire, session-level:** `client.WithTasksExtension()` adds `capabilities.extensions["io.modelcontextprotocol/tasks"] = {}` to the `initialize` request.
- **Stateless wire (or per-request override on legacy):** `_meta.io.modelcontextprotocol/clientCapabilities.extensions["io.modelcontextprotocol/tasks"]` on each request.

The server's `taskV2Middleware` checks both via [`core.ClientSupportsExtensionForRequest`](../core/stateless.go). See [`docs/MRTR_TUTORIAL.md` §4](MRTR_TUTORIAL.md#4-where-the-client-publishes-its-capability-menu--and-how-that-changes-per-wire) for the full capability-publishing story across wires.

### Registering the extension on the server

```go
import (
    "github.com/panyam/mcpkit/server"
    "github.com/panyam/mcpkit/ext/tasks"
)

srv := server.NewServer(info,
    server.WithRequestStateSigning([]byte(signingKey), 24*time.Hour),
)
srv.RegisterTool(...)
tasks.Register(tasks.Config{Server: srv})
```

`tasks.Register` does four things:

1. Installs `taskV2Middleware` on the server's middleware chain (intercepts `tools/call`).
2. Registers `tasks/get`, `tasks/update`, `tasks/cancel` as method handlers (gated on the extension).
3. Advertises the extension in the `initialize` response (legacy wire) and the SEP-2575 capabilities surface (stateless wire).
4. Wires up a default in-memory `TaskStore` if you didn't supply one.

---

## 4. The GoAsync sentinel + middleware peek

The 2026-05-19 design lock-in on [mcpkit#347](https://github.com/panyam/mcpkit/issues/347) (shipped in [PR 484](https://github.com/panyam/mcpkit/pull/484)) chose **Option 2**: the handler signals async escalation via an explicit sentinel on `ToolResult`, and the middleware peeks at the handler's return before deciding what to do.

### Why the inversion matters

Pre-Option-2, mcpkit's middleware minted the task *before* the handler ran, then dispatched the handler in a goroutine. That made one critical pattern impossible: a tool that wants to gather input via MRTR rounds *first* and then escalate to async would always emit `CreateTaskResult` on round 1, because the task was already minted before the handler could return `InputRequiredResult`.

Option 2 inverts this:

1. Middleware runs the handler **synchronously** via `next(ctx, req)`.
2. Looks at what came back via a type switch on `resp.Result`.
3. Dispatches:
   - `core.InputRequiredResult` → pass through; no task created
   - `core.ToolResult{GoAsync: true}` → mint task, spawn continuation goroutine, re-invoke handler with `TaskContext` attached, return `CreateTaskResult`
   - `core.ToolResult` (no GoAsync) → mint a born-terminal task, store the result, return `CreateTaskResult`

This is what unblocked the MRTR↔Tasks composition flow tested by the [`mrtr-tasks-composition`](https://github.com/panyam/mcpconformance) conformance scenario.

### The handler is a state machine

Because GoAsync is a `bool` sentinel (not a closure carrier), the goroutine **re-invokes the same handler** with the TaskContext plumbed in. The handler is a single function that branches on whether a TaskContext is present:

```go
func myHandler(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
    // Branch 3: re-invocation inside the continuation goroutine
    if tasks.GetTaskContext(ctx) != nil {
        return doRealWork(ctx, req)
    }

    // Branch 1: MRTR rounds (optional)
    if ctx.InputResponse("foo") == nil {
        return ctx.RequestInput(...)
    }

    // Branch 2: sync preflight done; escalate to async
    return core.ToolResult{GoAsync: true}, nil
}
```

For tools that *don't* need any MRTR preflight, the pattern shortens to "no TaskContext → GoAsync; have TaskContext → real work":

```go
func slowTool(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
    if tasks.GetTaskContext(ctx) == nil {
        return core.ToolResult{GoAsync: true}, nil
    }
    return doSlowWork(ctx, req)
}
```

This is the canonical pattern and what every fixture in [`examples/tasks-v2/main.go`](../examples/tasks-v2/main.go) uses.

> **Note.** The handler runs **twice** for any GoAsync `tools/call`: once sync (returns the sentinel), once in the goroutine (does the work). The TaskContext gate is what prevents side effects from double-firing. PR 484's Decision log and Risks sections call this out — a handler that does logging / metrics / DB writes on the non-GoAsync branch will fire twice unless gated.

### What happens to a sync handler on a `TaskSupport=optional` tool?

The middleware still creates a task — but the task is **born terminal**. `Status: completed`, result already stored, one `notifications/tasks` event fired. The wire response is still `CreateTaskResult`, so the client sees task shape; `tasks/get` returns the answer immediately. No goroutine runs.

The trade-off: the SEP-2663 G6 filter (no `notifications/progress` / `notifications/message` on tasks — see §8) is **not** applied on this path. The handler ran on the unfiltered POST ctx and is responsible for not emitting those notifications itself.

---

## 5. `TaskContext` — the in-task API

When the handler is running inside the continuation goroutine, [`tasks.GetTaskContext(ctx)`](../ext/tasks/runtime.go) returns a non-nil `*TaskContext` that gives access to task-scoped operations:

```go
type TaskContext struct {
    core.ToolContext  // embedded — keeps EmitProgress / EmitLog / etc. accessible
    // ...
}

func (tc *TaskContext) TaskID() string
func (tc *TaskContext) ProgressToken() any   // from _meta.progressToken; nil if not set
func (tc *TaskContext) SetStatus(status core.TaskStatus) error
func (tc *TaskContext) TaskElicit(req core.ElicitationRequest) (core.ElicitationResult, error)
func (tc *TaskContext) TaskSample(req core.CreateMessageRequest) (core.CreateMessageResult, error)
```

### `SetStatus`

Transitions the task's status and fires a `notifications/tasks` event. Use it to mark transitions other than the implicit `working → completed/failed` (e.g., when a long-running job hits an interesting milestone you want to surface). Status transitions enforce a state machine — see §6.

### `TaskElicit` and `TaskSample` — the in-task input flow

These are the equivalents of MRTR's `elicitation/create` and `sampling/createMessage` requests, but they happen **inside** the goroutine instead of before it. The goroutine blocks on a waiter channel; the client wakes it by sending `tasks/update`. See §7 for the full mechanic.

### What's missing from the v1 surface

Notably absent: any equivalent of v1's `ProgressToken` parameter on `SetStatus`, or v1's `EmitProgress`-via-task-channel. SEP-2663's G6 rule says tasks don't speak progress/message — surface progress through `SetStatus(...)` and `statusMessage` instead. See §8.

---

## 6. Task lifecycle — states + transitions

Tasks have five statuses and a strict transition graph:

```
              ┌─────────────────────┐
              │      working        │←──┐
              └──┬──────┬──────┬────┘   │  TaskElicit / TaskSample
                 │      │      │        │  delivered the response;
   completion    │      │      │        │  goroutine resumes
        normally │      │      │        │
                 ↓      ↓      ↓        │
          ┌──────┐  ┌──────┐  ┌──────────┐
          │compl.│  │failed│  │input_req.│
          │ ⊥    │  │  ⊥   │  │          │ ──┘
          └──────┘  └──────┘  └────┬─────┘
                                   │
                                   │ tasks/cancel before resume
                                   ↓
                                ┌──────┐
                                │cancel│
                                │  ⊥   │
                                └──────┘
```

**Transitions the spec allows:**

- `working → working` (allowed, but a no-op semantically)
- `working → input_required` (server-internal, fired automatically by `TaskElicit` / `TaskSample`)
- `input_required → working` (`tasks/update` delivers a response and unblocks the waiter)
- `working → completed` (handler returns a regular `ToolResult`)
- `working → failed` (handler returns a protocol-level error / middleware error / panic)
- `working → cancelled` (`tasks/cancel` arrives before terminal)
- `input_required → cancelled` (`tasks/cancel` while parked)

**Terminal statuses are sticky.** Once a task is `completed` / `failed` / `cancelled`, the store rejects further transitions (`errTaskTerminal`). This is what guards against cancel/complete races.

**Tool errors vs protocol errors:**

- A handler returning `core.ErrorResult(...)` (or `core.ToolResult` with `IsError: true`) → status `completed` with the error embedded in `result.isError`. Tool *output* is an error; task *execution* succeeded.
- A handler returning a non-nil Go error, or a panic, or a middleware error → status `failed` with the error in `DetailedTask.error`. Task *execution* itself failed.

This split matters for clients: `completed + isError:true` should be displayed to the user as a tool error; `failed` should be treated as an infrastructure issue.

---

## 7. The in-task input flow — pause, surface, resume

This is the symmetric counterpart of MRTR's `InputRequiredResult` round, scoped to *inside* a running task. **It's the answer to "what if I only discover I need more input *after* starting the work?"**

### The handler-side API

```go
func confirmDeleteHandler(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
    tc := tasks.GetTaskContext(ctx)
    if tc == nil {
        return core.ToolResult{GoAsync: true}, nil
    }

    // The goroutine actually BLOCKS here:
    result, err := tc.TaskElicit(core.ElicitationRequest{
        Message:         "Delete this file?",
        RequestedSchema: ...,
    })
    if err != nil {
        return core.ErrorResult(err.Error()), nil
    }

    if result.Action == "accept" && result.Content["confirm"] == true {
        return core.TextResult("deleted"), nil
    }
    return core.TextResult("kept"), nil
}
```

The call reads as synchronous, but under the hood:

1. `TaskElicit` mints a stable key (`elicit-1`, `elicit-2`, ...), stashes the request on the task's `inputState`, and returns a per-key waiter channel.
2. The task's status is updated to `input_required` and a `notifications/tasks` event fires (so clients listening on the SSE stream see the transition).
3. The goroutine `<-waiter` blocks. `ctx.Done()` is honored — if the task is cancelled, the wait unblocks with the context error.
4. The client observes the pending input via `tasks/get` (which surfaces `inputState.snapshot()` on `DetailedTask.InputRequests`).
5. The client sends `tasks/update` with the matching key in `inputResponses`.
6. The server delivers the payload to the waiter channel.
7. The goroutine unblocks, `TaskElicit` returns, the handler resumes.

### The wire choreography

```
═══ Inside the goroutine ════════════════════════════════════════════════════
handler calls tc.TaskElicit(...)
       ↓
       inputState.Enqueue("elicit", request) → returns (key="elicit-1", waiter)
       store.Update: status → input_required
       notifyV2TaskStatus(...) → emits notifications/tasks
       ↓
       <-waiter   ← BLOCKS HERE until tasks/update arrives (or ctx.Done())

═══ Client side, in parallel ════════════════════════════════════════════════
client → POST tasks/get {taskId}
client ← DetailedTask{
            status: "input_required",
            inputRequests: {
              "elicit-1": { method: "elicitation/create", params: {...} }
            }
         }
         ↑ client renders the elicitation to the user, gets an answer

client → POST tasks/update {
           taskId,
           inputResponses: {
             "elicit-1": { action: "accept", content: {...} }
           }
         }
client ← {}   ← empty ack; the handler continues asynchronously

═══ Back in the goroutine ═══════════════════════════════════════════════════
       ↓
       <-waiter unblocks with the payload
       store.Update: status → working
       handler resumes inside TaskElicit, returns the parsed ElicitationResult
       ↓
       ... handler may call TaskElicit again, or finish ...
       ↓
       store.StoreTerminalResult(... TaskCompleted ...)
       notifyV2TaskStatus(...) → final notifications/tasks event
```

### Parallel fan-out

A handler can call multiple `TaskElicit` / `TaskSample` operations from concurrent goroutines; the task surfaces *all* pending keys under `inputRequests`, and `tasks/update` can deliver them partially or in any order. mcpkit's `multi_input` fixture exercises this:

```go
var wg sync.WaitGroup
wg.Add(2)
go func() {
    defer wg.Done()
    nameRes, nameErr = tc.TaskElicit(nameRequest)
}()
go func() {
    defer wg.Done()
    confirmRes, confirmErr = tc.TaskElicit(confirmRequest)
}()
wg.Wait()
```

The conformance scenario for partial fulfillment asserts that the client can answer one key, see the task is *still* `input_required` (because the other key is still pending), then answer the second.

### Map keys are server-chosen and opaque

mcpkit picks readable keys (`elicit-1`, `sample-2`) for debuggability, but the keys are a server-internal convention. Per SEP-2663 / SEP-2322 the wire contract is *"keys are opaque echo strings — clients MUST NOT parse them."* We're free to change the generator (e.g., to UUIDs) without breaking any conformant client.

---

## 8. Notifications — `notifications/tasks` and the G6 filter

### `notifications/tasks`

The lifecycle event stream. Server emits one whenever a task's status transitions; the payload is a full `DetailedTask` (status, result if completed, error if failed, inputRequests if input_required, etc.). Wire-shape-identical to the response of `tasks/get`.

On the legacy wire, these fan out on the persistent GET SSE stream. On the stateless wire, they fan out on `subscriptions/listen` (when the client opted into a listener) — note: stateless support for `notifications/tasks` lands in follow-up work; today the stateless path silently drops the emission, matching the spec's "no server-initiated push" baseline.

### The G6 filter — what gets dropped inside a task

SEP-2663 G6: **a task's notification channel is reserved for `notifications/tasks`**. Two notifications that work fine on sync tools are forbidden inside tasks:

- `notifications/progress` — was used for streaming progress %. Replacement: `tc.SetStatus(...)` + `statusMessage` on `TaskInfo`. Clients observe progress via `tasks/get` polling or the `notifications/tasks` stream.
- `notifications/message` — was used for streaming server-side log emissions. Replacement: structured `result.content` when the task completes (final output), or out-of-band server-side logging for live observability (your own log infra, OpenTelemetry, etc.). The spec is "MCP isn't the transport for that; use real logging."

mcpkit enforces this in [`ext/tasks/tasks.go`](../ext/tasks/tasks.go):

```go
// SEP-2663 G6: notifications/progress and notifications/message MUST
// NOT be sent on tasks. Filter at the session-notify boundary so any
// tool handler that calls EmitProgress or EmitLog while it happens to
// be running as the GoAsync continuation silently no-ops rather than
// leaking onto the session stream.
bgCtx = core.ApplySessionNotifyFilter(bgCtx,
    "notifications/progress",
    "notifications/message",
)
```

So a handler written for the pre-G6 world doesn't break — it just stops emitting those notifications when running as the GoAsync continuation. The migration is mechanical, not behavioral.

### Filter scope is goroutine-only

The filter is applied **only** to the continuation goroutine's `bgCtx`. A sync handler returning a `ToolResult` (or running an MRTR round) on a `TaskSupport=optional/required` tool runs on the **unfiltered** POST ctx — `EmitProgress` and `EmitLog` work normally there. This is a deliberate narrowing documented in PR 484's Decision log: sync handlers are responsible for not leaking notifications they shouldn't.

For deeper coverage of how `progressToken` works across both paths, see [`docs/MRTR_TUTORIAL.md` §5](MRTR_TUTORIAL.md#5-progresstoken--who-mints-it-and-what-its-for).

---

## 9. Cancellation

`tasks/cancel` triggers cancellation of the goroutine via the bgCtx that was attached when the task spawned. The flow:

1. Client sends `tasks/cancel {taskId}`. Returns an empty ack immediately (`{}`).
2. Server calls `cancelFunc()` on the bgCtx (set up in `spawnGoAsyncTask`).
3. The goroutine's `ctx.Done()` channel closes. Inside the handler, any `select` on `ctx.Done()` unblocks.
4. The handler returns. The middleware's deferred recovery sees the cancellation and records terminal status `cancelled`.
5. Final `notifications/tasks` event fires.

### Handler responsibilities

- **Long-running blocking work** should `select` on `ctx.Done()` alongside whatever else it's waiting on. The `slow_compute` fixture demonstrates this pattern.
- **`TaskElicit` / `TaskSample`** already honor `ctx.Done()` — the waiter `<-` unblocks with the context error if the task is cancelled while parked.
- **Cleanup** should be in `defer` blocks. The middleware's recovery handles panics; you handle resource cleanup.

### Cancel-during-input-required

A particularly important case: a task parked in `input_required` (waiting on `TaskElicit`) gets cancelled by the client. The waiter `<-` unblocks with the context error; `TaskElicit` returns a non-nil error; the handler returns; status transitions to `cancelled`. The TestV2_ElicitCancelUnblocks fixture in `ext/tasks/tasks_test.go` exercises this.

---

## 10. Tasks vs MRTR — when to use which, and how they compose

Two distinct mechanisms for "server-asks-client-for-something," scoped to two different phases of a tool's lifetime. See [`docs/MRTR_TUTORIAL.md` §7](MRTR_TUTORIAL.md#7-when-to-use-what--mrtr-vs-push-vs-task-input-flow) for the full comparison table and decision flow; the short version:

| Aspect | MRTR (`ctx.RequestInput`) | Task input flow (`tc.TaskElicit`) |
|---|---|---|
| **When** | Before task escalation, during sync preflight | After task escalation, inside the goroutine |
| **Wire** | Client re-invokes the same `tools/call` | Client polls via `tasks/get`, delivers via `tasks/update` |
| **Server state across rounds** | None — stateless continuation token | Lots — `activeTask` + `inputState` + parked goroutine |
| **Restartable across replicas** | Yes — token carries everything | No — goroutine is pinned to one process |
| **Best for** | "I know I'll need input before I can decide what to do" | "I started the work and only discovered I need more input" |

### Composition: MRTR + GoAsync + in-task input

The killer pattern — and what [PR 484](https://github.com/panyam/mcpkit/pull/484) unblocked — is a single tool that uses all three:

```go
func compositeHandler(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
    // PHASE 3: in the continuation goroutine
    if tc := tasks.GetTaskContext(ctx); tc != nil {
        // Maybe call TaskElicit mid-work if something new comes up
        confirmation, err := tc.TaskElicit(midJobConfirmation)
        if err != nil {
            return core.ErrorResult(err.Error()), nil
        }
        if confirmation.Action != "accept" {
            return core.TextResult("aborted at mid-job confirmation"), nil
        }
        return finishExpensiveWork(ctx, req)
    }

    // PHASE 1: MRTR — gather upfront input
    if ctx.InputResponse("api_key") == nil {
        return ctx.RequestInput(needApiKey)
    }
    if ctx.InputResponse("target") == nil {
        return ctx.RequestInput(needTarget)
    }

    // PHASE 2: preflight done; escalate to async
    return core.ToolResult{GoAsync: true}, nil
}
```

Three orthogonal mechanisms, one handler. The key spec separations (asserted by the `mrtr-tasks-composition` conformance scenario, see [`conformance/mrtr/scenarios.test.ts`](../conformance/mrtr/scenarios.test.ts)):

- MRTR `requestState` does **not** flow into the task's `requestState`. Each MRTR phase has its own ephemeral continuation; the task has its own per-task `inputState` keyed by `taskID`.
- MRTR `inputRequests` keys live in the round's `requestState` token; task `inputRequests` keys (`elicit-1`, etc.) live in the task's `inputState`. They never collide because they live in different wire envelopes.
- Clients don't have to dedup across the two flows. Round 2 goes on `tools/call`; the task answer goes on `tasks/update`. The wire shape tells the client which it is.

### When you don't need MRTR

If your tool just needs to run a long-blocking computation with no upfront input, skip MRTR entirely:

```go
func longRunner(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
    if tasks.GetTaskContext(ctx) == nil {
        return core.ToolResult{GoAsync: true}, nil
    }
    return runForAWhile(ctx, req)
}
```

### When you don't need a task

If your tool just needs to gather input once and then return synchronously, skip the task escalation:

```go
func quickInteractive(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
    if ctx.InputResponse("answer") == nil {
        return ctx.RequestInput(askForAnswer)
    }
    return core.TextResult(buildResult(ctx.InputResponse("answer"))), nil
}
```

(Register without `Execution.TaskSupport` set — pure sync, no extension dependency.)

---

## 11. Multi-tenancy on the stateless wire

A production-deployment note worth knowing upfront.

[`server/stateless_backend.go:236-240`](../server/stateless_backend.go) documents the current state:

> All stateless task store entries currently key under sessionID=""
> (no session). This means stateless tasks share one bucket per process,
> which is acceptable for the single-tenant fixtures the conformance
> suite covers; multi-tenant deployments should layer an auth-subject-
> keyed store wrapper. Tracked for a follow-up.

What this means in practice:

- On the legacy wire, every request carries an `Mcp-Session-Id` header → `core.GetSessionID(ctx)` returns the live session ID → the task store buckets tasks per session → users can't see each other's tasks.
- On the stateless wire, there is no session → `core.GetSessionID(ctx)` returns `""` → all tasks land in the `""` bucket → in a multi-tenant deployment, every user's tasks share the same bucket.

Single-tenant deployments (one user per process, demos, conformance fixtures) are unaffected. Multi-tenant deployments have a real isolation hole today: `tasks/get`, `tasks/cancel`, and `tasks/update` look up by `(taskID, sessionID)`, and with `sessionID=""` across users they can read / cancel / update each other's tasks.

The fix is tracked in [issue 485](https://github.com/panyam/mcpkit/issues/485) — a `TaskBucketKeyer` seam that lets deployers derive the bucket key from an auth subject (or any other request attribute) without `ext/tasks` taking a hard dependency on `ext/auth`. Until that lands, multi-tenant stateless deployments should layer their own keyed-store wrapper.

---

## 12. Quick reference

### Server setup

```go
import (
    "github.com/panyam/mcpkit/server"
    tasks "github.com/panyam/mcpkit/ext/tasks"
)

srv := server.NewServer(info,
    server.WithRequestStateSigning([]byte(signingKey), 24*time.Hour),  // for MRTR too
)

srv.RegisterTool(
    core.ToolDef{
        Name:        "slow_compute",
        Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportOptional},
        InputSchema: ...,
    },
    handler,
)

tasks.Register(tasks.Config{
    Server:        srv,
    DefaultTTLMs:  5 * 60 * 1000,   // 5 minutes
    DefaultPollMs: 1000,             // 1 second
    // Store: ... custom TaskStore impl; defaults to in-memory
})
```

### Handler patterns

```go
// Slow / blocking work, no MRTR input
func slowOnly(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
    if tasks.GetTaskContext(ctx) == nil {
        return core.ToolResult{GoAsync: true}, nil
    }
    return doSlowWork(ctx, req)
}

// MRTR-then-async (the composition pattern)
func mrtrThenAsync(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
    if tasks.GetTaskContext(ctx) != nil {
        return doRealWork(ctx, req)
    }
    if ctx.InputResponse("foo") == nil {
        return ctx.RequestInput(needFoo)
    }
    return core.ToolResult{GoAsync: true}, nil
}

// In-task input mid-execution
func midTaskElicit(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
    tc := tasks.GetTaskContext(ctx)
    if tc == nil {
        return core.ToolResult{GoAsync: true}, nil
    }
    result, err := tc.TaskElicit(elicitRequest)
    if err != nil {
        return core.ErrorResult(err.Error()), nil
    }
    return processResult(result)
}
```

### Client patterns

```go
import client "github.com/panyam/mcpkit/client"

c := client.NewClient(url, info, client.WithTasksExtension())
if err := c.Connect(); err != nil { ... }

// Tool call may return a CreateTaskResult
ctr, err := client.ToolCall(c, "slow_compute", args)
// ...

// Wait for terminal
dt, err := client.WaitForTask(ctx, c, ctr.TaskID)

// Or poll yourself
dt, err := client.GetTask(c, ctr.TaskID)

// Deliver mid-task input
err := client.UpdateTask(c, taskID, inputResponses)

// Cancel
err := client.CancelTask(c, taskID)
```

### Errors to expect

| Error | When | Action |
|---|---|---|
| `-32003 missing required client capability` (`io.modelcontextprotocol/tasks`) | Tool has `TaskSupport=required` but client didn't declare the extension | Add `client.WithTasksExtension()` or per-request `_meta.clientCapabilities.extensions` |
| `-32602 request params missing required _meta envelope` | Stateless request without `_meta` | Add the SEP-2575 `_meta` envelope on every stateless request |
| `task not found` | `tasks/get` after the TTL has expired | Reduce `pollIntervalMs` or use `WaitForTask` to avoid hitting the TTL window |
| `errTaskTerminal` (server-side) | Trying to transition a terminal task (e.g., cancel-after-completed race) | Expected; treat as benign, the task already completed |

---

## See also

- [`docs/MRTR_TUTORIAL.md`](MRTR_TUTORIAL.md) — sibling tutorial for SEP-2322 MRTR (capabilities across wires, progressToken, push-vs-MRTR-vs-task-input decision flow).
- [`docs/TASKS_V2_MIGRATION.md`](TASKS_V2_MIGRATION.md) — v1 → v2 migration guide.
- [`ext/tasks/README.md`](../ext/tasks/README.md) — task extension API reference.
- [`docs/SEP_2663_TASKS_CONFORMANCE_PLAN.md`](SEP_2663_TASKS_CONFORMANCE_PLAN.md) — conformance plan + status.
- [`examples/tasks-v2/main.go`](../examples/tasks-v2/main.go) — six task fixtures (slow_compute, failing_job, confirm_delete, multi_input, protocol_error_job, external_job) all using the GoAsync pattern.
- [`examples/mrtr/main.go`](../examples/mrtr/main.go) — eight MRTR fixtures including `test_tool_with_task` for the composition pattern (A8).
- [panyam/mcpconformance](https://github.com/panyam/mcpconformance), branch `feat/tasks-mrtr-extension` — SEP-2663 + SEP-2322 conformance scenarios.
- [Issue 452](https://github.com/panyam/mcpkit/issues/452) — stateless wire MRTR support follow-up.
- [Issue 485](https://github.com/panyam/mcpkit/issues/485) — multi-tenant isolation for stateless task store follow-up.
