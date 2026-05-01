# MCP Tasks v2 Conformance Suite (SEP-2663)

Tests any MCP server that implements the Tasks v2 surface, evolving from the original SEP-2557 draft to the current SEP-2663 (Tasks Extension) shape with SEP-2322 (MRTR base types), SEP-2575 (per-request capabilities), and SEP-2243 (Mcp-Name HTTP header).

The suite drives wire-shape and behavioral assertions against any conformant server using the official [MCP TypeScript SDK](https://github.com/modelcontextprotocol/typescript-sdk) client plus raw `fetch` for assertions the SDK's typed schemas would strip.

**Current result against the in-repo server:** 27/27 scenarios passing.

## Specs covered

| SEP | What it adds | Where it shows up |
|-----|--------------|-------------------|
| SEP-2663 | Tasks Extension — `io.modelcontextprotocol/tasks` capability, `DetailedTask`, `tasks/update`, ack-only `tasks/cancel`, wire-field renames (`ttlSeconds`, `pollIntervalMilliseconds`) | v2-02, v2-04, v2-07, v2-09, v2-10, v2-11, v2-12, v2-17, v2-22, v2-23 |
| SEP-2322 | MRTR base types — `inputRequests`/`inputResponses` maps, `requestState`, `resultType` discriminator (`task`/`complete`/`incomplete`) | v2-14, v2-15, v2-16, v2-17, v2-26 |
| SEP-2575 | Per-request capability override via `_meta.io.modelcontextprotocol/clientCapabilities` | v2-25 |
| SEP-2243 | `Mcp-Name` HTTP response header on task-creating responses | v2-24, v2-24b |

These are deliberately kept in **one file** rather than split per-SEP. The test harness (initialize handshake, raw fetch session, extension declaration, tool fixtures) is identical for every scenario; splitting would duplicate the harness without behavioral gain. If a future non-tasks consumer of SEP-2322 appears (e.g., MRTR-style input rounds outside the tasks surface), that's the trigger to extract a separate `conformance/mrtr-base/` suite.

## Wire-format diff vs v1

| Aspect | v1 (spec 2025-11-25) | v2 (SEP-2663) |
|--------|----------------------|---------------|
| Capability slot | `capabilities.tasks` | `capabilities.extensions["io.modelcontextprotocol/tasks"]` |
| Client opt-in | (none — anyone can send `task` hint) | MUST declare extension at session OR per-request (SEP-2575) |
| Task creation | Client sends `task` hint param | Server decides unilaterally |
| `resultType` discriminator | absent | `"task"` (CreateTaskResult) / `"complete"` (everything else) |
| `CreateTaskResult` shape | `{task: {…}}` (nested) | `Result & Task` — flat: `{resultType, taskId, status, ttlSeconds, …}` (no nested `task` wrapper) |
| `tasks/get` response | flat `TaskInfo` only | `DetailedTask` with inlined `result`/`error`/`inputRequests`/`requestState` |
| `tasks/update` | n/a | new — MRTR resume path, returns `{resultType:"complete"}` ack |
| `tasks/cancel` response | rich task envelope | `{resultType:"complete"}` ack (no task state) |
| `tasks/result` | separate blocking method | **removed** (result inlined on `tasks/get`) |
| `tasks/list` | session-scoped list | **removed** |
| TTL field | `ttl` (ms by convention) | `ttlSeconds` (units in name) |
| Poll-interval field | `pollInterval` | `pollIntervalMilliseconds` |
| `parentTaskId` | present | removed |
| Tool errors | `status:failed` | `status:completed, result.isError:true` |
| Protocol errors | `status:failed, error:...` | `status:failed, error:{code,message,data}` |
| `Mcp-Name` HTTP header | not set | set on task-creating responses (SEP-2243) |
| `requestState` integrity | n/a | optionally HMAC-SHA256 signed via `TasksConfig.RequestStateKey` |

## Required server fixtures

The target server MUST register these tools:

| Tool | Behavior | Used by |
|------|----------|---------|
| `greet` | Sync — returns `Hello, {name}!` | v2-01, v2-26 |
| `slow_compute` | Async — `seconds`-second sleep, returns result; `seconds:0` for immediate | v2-02 through v2-04, v2-07, v2-08, v2-12, v2-13, v2-14, v2-15, v2-21, v2-23, v2-24, v2-24b, v2-25, v2-26 |
| `failing_job` | Async — always returns tool error after ~1s | v2-05, v2-19 |
| `protocol_error_job` | Async — panics, surfaces as protocol error | v2-06 |
| `confirm_delete` | Async — calls `TaskElicit` to drive the MRTR loop | v2-16, v2-17, v2-26 |

The in-repo `examples/tasks-v2` server registers all of these.

## Setup

```bash
cd conformance && npm install
```

## Usage

Self-contained run (builds the example server, starts it, runs the suite, tears down):

```bash
make testconf-tasks-v2
```

Manual run against a server you started yourself:

```bash
SERVER_URL=http://localhost:8080/mcp npx tsx --test tasks-v2/scenarios.test.ts
```

## Scenarios

### Polymorphic dispatch + lifecycle (SEP-2663 + SEP-2322)

| # | Scenario | What it tests | SEPs |
|---|----------|---------------|------|
| 01 | Sync tool call | `greet` returns immediately, no task | 2663 |
| 02 | Server-directed task creation | Server creates task without client `task` param | 2663 |
| 03 | tasks/get working status | Poll an active task | 2663 |
| 04 | tasks/get completed + inlined result | Poll completed task — `result` inlined | 2663 |
| 05 | Tool error → completed + isError | Tool execution errors carry `isError:true` | 2663 |
| 06 | Protocol error → failed + error | Server-side protocol errors carry `error:{code,message}` | 2663 |
| 07 | tasks/cancel (empty ack) | Cancel response is `{resultType:"complete"}`; status settles to cancelled | 2663 |
| 08 | Cancel terminal task | Cancel completed task → `-32602` | 2663 |

### Removed v1 methods

| # | Scenario | What it tests | SEPs |
|---|----------|---------------|------|
| 09 | No `tasks/result` | `-32601` MethodNotFound | 2663 |
| 10 | No `tasks/list` | `-32601` MethodNotFound | 2663 |

### Capability negotiation

| # | Scenario | What it tests | SEPs |
|---|----------|---------------|------|
| 11 | Tasks under `capabilities.extensions` | Extension advertised; v1 `capabilities.tasks` slot stays absent | 2663 |
| 22 | `tasks/*` rejected without extension | `-32601` for clients that didn't negotiate | 2663 |
| 23 | `tools/call` without extension | Returns sync `ToolResult` (`resultType:"complete"`, no task) | 2663 + 2322 |
| 25 | Per-request `_meta` opt-in | Produces `CreateTaskResult` even without session-level extension | 2575 |

### TTL + wire fields

| # | Scenario | What it tests | SEPs |
|---|----------|---------------|------|
| 12 | `ttlSeconds` present | Renamed from v1 `ttl`; v1 key absent | 2663 |
| 13 | No early TTL expiry | Task accessible before TTL elapses | 2663 |

### requestState (SEP-2322)

| # | Scenario | What it tests | SEPs |
|---|----------|---------------|------|
| 14 | Server returns `requestState` | tasks/get may include it | 2322 |
| 15 | Client echoes `requestState` | Subsequent tasks/get accepts it | 2322 |

### MRTR — input flow (SEP-2322 + SEP-2663)

| # | Scenario | What it tests | SEPs |
|---|----------|---------------|------|
| 16 | `inputRequests` map on `tasks/get` | `input_required` status surfaces pending requests | 2322 |
| 17 | `tasks/update` resumes task | Client delivers responses via tasks/update; ack is `{resultType:"complete"}` | 2322 + 2663 |

### Notifications

| # | Scenario | What it tests | SEPs |
|---|----------|---------------|------|
| 18 | DetailedTask in notifications | Terminal notification includes inlined result | 2663 |

### Misc

| # | Scenario | What it tests | SEPs |
|---|----------|---------------|------|
| 19 | No client `task` param | tools/call without `task` still creates task | 2663 |
| 20 | Immediate result shortcut | Fast operation may skip task creation | 2663 |
| 21 | No `related-task` `_meta` on inlined result | tasks/get's inlined `result` doesn't carry the v1 _meta key | 2663 |

### SEP-2243: Mcp-Name HTTP header

| # | Scenario | What it tests | SEPs |
|---|----------|---------------|------|
| 24 | Mcp-Name on task creation | Task-creating tools/call response carries `Mcp-Name: <taskId>` | 2243 |
| 24b | Mcp-Name absent on sync | Sync tools/call response does NOT carry the header | 2243 |

### SEP-2322: resultType discriminator across non-task responses

| # | Scenario | What it tests | SEPs |
|---|----------|---------------|------|
| 26 | `resultType:"complete"` on non-task responses | Sync tools/call, tasks/get, tasks/update ack, tasks/cancel ack all carry the discriminator | 2322 |

## Open spec questions

Where the spec is silent or ambiguous, mcpkit picks the louder/safer option (typically `-32602` over silent ack), pins the choice with an inline `// Open spec question` comment in code, and names the corresponding conformance scenario as the change-detector if the spec settles differently.

The living register is tracked in the GitHub issue titled "Tracking: SEP-2322 / SEP-2663 / SEP-2575 open spec questions" (issue 339). Three entries today:

1. **`requestState` rejection** — silent ack vs `-32602`. We picked `-32602`. Pinned in `server/tasks_v2.go:verifyRequestState`; covered by `TestV2_RequestState_TamperedRejected`.
2. **SEP-2575 per-request capabilities shape** — using opaque `json.RawMessage` envelope so the shape can change without consumer churn. Covered by v2-25.
3. **`tasks/update`/`tasks/cancel` ack for unknown taskId** — silent ack vs `-32602`. We picked `-32602`. Pinned in `server/tasks_v2.go:makeV2UpdateHandler`; covered by `TestV2_UpdateUnknownTaskRejected` (server-side; no conformance scenario yet — the upstream spec wording is too soft to assert against).

## Design notes

### Assertions follow v1 lessons

Based on spec maintainer feedback on the v1 suite:
- Error codes use `assertJsonRpcError(e, code, label, enforce?)` with `ENFORCE_ERROR_CODES = false` by default
- `enforce = true` only for cases where the code is mandated (e.g., `-32601` for unknown methods)
- TTL assertions check reasonable ranges, not exact values
- Notifications are optional — well-formed if received

### Two raw fetch sessions

`before()` initializes two raw HTTP sessions:
- `sessionId` — declares `io.modelcontextprotocol/tasks` extension; used by every happy-path scenario
- `unsupportedSessionId` — does NOT declare the extension; used by gating tests (v2-22, v2-23)

This avoids server-side state leakage between scenarios that need different capability profiles.

### Bypassing SDK schema validation

The MCP TypeScript SDK ships with strict Zod schemas that strip v2-only fields (`result`, `error`, `inputRequests`, `requestState`) from responses. Every scenario that needs to read those fields uses raw `fetch` via the `rawRequest` / `rawRequestFull` helpers rather than the typed SDK client. SDK-typed assertions are reserved for things the SDK already understands (e.g., the initialize negotiation in `before()`).

### Shared helpers

Common utilities (`assertJsonRpcError`, `waitForTerminal`, `waitForStatus`) are in `conformance/common/helpers.ts` and shared with the v1 suite.
