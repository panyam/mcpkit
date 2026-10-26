# server/ — implementation notes

Dispatch and protocol lore. For the public API see `README.md`; for enforceable rules see
`CONSTRAINTS.md`.

---

## Version negotiation and version-gated features

**`server/protocol_features.go` is the single source of truth.** Do not scatter fresh
`negotiatedVersion == "..."` checks anywhere else, which is what let the legacy and stateless
wires drift apart before.

- `negotiateProtocolVersion(requested)` implements the MCP 2025-03-26 handshake. A supported
  version is echoed. An **unsupported** one falls back to the server's preferred (latest)
  supported version and replies with it, rather than erroring `-32602`. An *absent*
  `protocolVersion` is still rejected as malformed.
- `featuresForVersion(v) ProtocolFeatures` plus `d.protocolFeatures()` resolve which version-gated
  behaviors are on. Currently `RoutingHeaderValidation` (SEP-2243) and `StatelessMetaRequired`
  (SEP-2575). **Add a field here** rather than a new inline check.
- A duplicate `initialize` on a negotiated session is rejected `-32600` with state preserved. Opt
  back in with `server.WithAllowReinitialize()`; the `initDispatcher` test helper sets
  `allowReinitialize=true` so capability-inspection tests can re-init.

**Every dated revision goes in `supportedProtocolVersions`, and a gap fails quietly.** #57 closed
as "implemented" with 2025-06-18 left out, and it stayed out until #1519. Nothing errored. A client
pinned to it was negotiated up to the draft line, which the spec allows, then failed every later call
for missing `_meta`, and a Streamable HTTP client sending it in `MCP-Protocol-Version` got a 400. Our
own clients and fixtures all sent 2025-11-25, so no test noticed. `TestDispatchInitializeEchoesDatedVersions`
now pins the dated list.

**JSON-RPC batching is version-gated.** 2025-03-26 added it and 2025-06-18 removed it, so
`ProtocolFeatures.BatchingRemoved` is set for 2025-06-18 and later. Both HTTP transports check it
before splitting the array. Streamable HTTP answers 400 with one `-32600`; the legacy SSE transport
pushes the same error on the stream and returns 202, like its parse errors. No element of a refused
batch runs. Older sessions keep the permissive path, and the stdio transport has never accepted
batches at all.

The stateless wire (`Server.dispatchStateless`, `server/stateless_dispatch.go`) still has its own
version-error path. Fold it through the resolver when #493 gets to it.

---

## Stateless-wire dispatch parity — the recurring failure mode

**The stateless wire calls the session dispatcher's own handlers for `tools/call`,
`prompts/get`, `resources/read` and `completion/complete`**, passing `wireStateless`
(#1637). Where the wires really differ, the handler branches on `wire`. Grep for
`wire == wireStateless` to find every such difference. Today there are two: the
`-32021` mapping of `*core.MissingCapabilityError` in `handleToolsCall`, and the
features cache hints are shaped for in `applyReadCacheControl`.

Until #1637 each of those methods had a hand-written copy in
`server/stateless_backend.go`, and every dispatch feature added to the session handler
had to be mirrored by hand. Forgetting worked on the session wire and failed silently
on stateless, and conformance usually missed it because the task and file-input
suites run on the session wire while the stateless suite uses the cart fixture. Three
landed that way before anyone noticed the pattern (MRTR, SEP-2356 file inputs, the
client-side SEP-2243 `Mcp-Name` header for task ops). agni then found schema
validation missing (#1637), and an audit of the four copies turned up six more:
prompt argument validation, per-tool / per-prompt / per-resource / per-template
timeouts, the server-wide `WithToolTimeout` (it lived in the session-only terminal
in `server.go`), the `MaxCompletionValues` cap, and handler error messages without
their `tool "x":` style prefix. The `1637` rows in `TestWireParity` cover them.

`make verify-dual` (#478) drives example walkthroughs over both wires to catch this class; see
`examples/DUAL_MODE_AUDIT.md`.

**Both wires run one request path** (#1625). `Server.runRequest` builds the per-request context
and the chain (user middleware, then metrics, then trace outermost) around a terminal, and both
wires come through it: the session wire from `dispatchWithOpts` with `wireLegacy`, the stateless
wire from `Server.dispatchStateless` with `wireStateless`. What only a session has
(notify and request push with their interceptors, `ContextWithSession`, the session id, the
detach strategy, the SSE retry hint) sits inside `if wire == wireLegacy`. Everything after it,
the task bucket keyer, `Mcp-Method`, allowed roots, `NotifyResourceUpdated` and the content-chunk
method, applies to both. `dispatchStateless` keeps only what is really the stateless wire's own
(the removed-methods `-32601`, the `_meta` and version checks, serverInfo and resultType stamping)
and then calls `runRequest` for every method, `server/discover` included, so a middleware sees
`server/discover` the way it sees `initialize` on the session wire.

That dispatcher used to live in `server/stateless` behind a `Backend` interface, because `server`
imports `stateless` and not the other way round, and `statelessBackend` existed only to satisfy
it. Once everything past the envelope checks was `runRequest` and the shared method table, the
interface was glue, so #1625 moved the dispatcher into `server/stateless_dispatch.go` and deleted
`Backend`, `New`, `Dispatcher` and the adapter. `server/stateless` keeps what callers import it
for: `Mode`, `DiscoverResult`, the `subscriptions/listen` frames, `HTTPStatusForCode` and
`RequestMetaFromContext`.

Until #1625 the stateless chain was opt-in per handler in `server/stateless/handlers.go`, the
opposite of the session wire. That is how `resources/read` (#1352) and the list methods (#1355)
skipped it, and it also meant a stateless request never met the metrics or trace middleware, the
content-chunk method, `NotifyResourceUpdated`, or `WithAllowedRoots`. The last one mattered:
`core.IsPathAllowed` answers true when nothing is installed, so a stateless caller got past a
server's static roots. `SetAllowedRoots` and `SetNotifyResourceUpdated` put their function on ctx
when there is no session now. A handler panic also went unrecovered on stateless, which over stdio
would take the process down; the terminal defers `recoverHandlerPanic` like the session `Dispatch`.

**Both wires share one method table**, `Dispatcher.route(ctx, wire, id, req)`. The session
`Dispatch` calls it once the session is initialized, and the stateless terminal calls it with
`wireStateless`. The arms that differ branch on `wire`: `server/discover` answers only on the
stateless wire (a session has `initialize`), `resources/(un)subscribe` and `logging/setLevel` only
on the session wire, and the list handlers take their cache-hint feature set and, for `tools/list`,
the fileInputs declaration from the wire. Before this the stateless terminal had its own switch,
whose `resources/list`, `resources/templates/list` and `prompts/list` ignored the cursor. Nothing
paginates by default (#1356), so no test could see it, but the first page size anyone set would
have split the wires. `WithNotifyInterceptor` now wraps the stateless SSE notify func too, through
the same `Server.interceptNotify` the session path uses.

The rule for wire differences, set 2026-10-24: the session wire stays through roughly 18 months of
deprecation, so seeing where the wires differ matters more than code that never branches. Prefer
one shared handler with an explicit `if wire == …` where behaviour differs over a second per-wire
handler. A grep for the branch finds every parity difference, a feature missing on one wire can't
hide as code nobody wrote (which is how #1355 lived), and retiring the session wire is deleting the
session arms.

`make exercise-wire` (#1555) checks this class. `server/wireparity_test.go` sends
the same 2026-07-28 requests over both wires and compares, with `knownWireDifferences` naming every
key allowed to differ. It needs two single-wire handlers, because a dual-mode handler routes any
request whose `_meta` carries a `protocolVersion` to the stateless dispatcher, session id or not.
So under the default `ModeDual` a 2026-07-28 client never reaches the session wire at all; only
`ModeLegacyOnly` serves a session at that version.

### Typed middleware errors on the stateless wire (#815, PR 816)

A `server.Middleware` that short-circuits with `*core.AuthError` (SEP-2350 scope challenge, step-up
auth) now reaches the transport's shared `writeAuthError`, emitting HTTP 403 plus
`WWW-Authenticate`. Previously it was folded into a generic `-32603` / HTTP 200, losing both.

Mechanism: `Server.dispatchStateless` returns `(*core.Response, error)`, and the error is the
chain's short-circuit, forwarded raw with a nil response. `handleStatelessPost` and
`handleStatelessPostSSE` call `writeAuthError` on a non-nil dispatch error. The SSE path is safe
because middleware short-circuits before any frame is written, so `sseStarted` is always false.

**Behavior-parity note**: this routes *all* stateless middleware errors through `writeAuthError`,
so a non-`AuthError` middleware error now maps to HTTP 401 (matching the legacy wire) instead of
`-32603` / HTTP 200.

---

## Wire mode defaults are deliberately asymmetric

- Server: `stateless.DefaultMode = stateless.ModeDual`. Additive on upgrade, so every existing
  server gains the stateless wire on one URL.
- Client: `client.DefaultClientMode = client.ClientModeLegacyOnly`. Conservative, since `Adaptive`
  would have silently broken 11 pre-existing client tests that assume the legacy initialize
  handshake.

Override per deployment via constructor option (`server.WithStatelessMode(...)` /
`client.WithClientMode(...)`), env var (`MCPKIT_STATELESS_MODE` / `MCPKIT_CLIENT_MODE`), or an
`init()` flip of the package var. The shipping client default may flip to `Adaptive` in a future
major release; the doc block spells out the migration.

This asymmetry was empirically re-confirmed by the client conformance harness, where a blanket
`ClientModeAdaptive` breaks legacy mocks in the same suite.

**stdio is always dual** (#1637). It routes each message with `wireKindFromMessage`, the
header-free half of `detectWireKind`: `initialize` goes to the session, `server/discover` or a
`_meta.protocolVersion` goes to the stateless dispatcher, and anything else to the session. There
is no `stateless.Mode` for stdio, because a stdio server has one client and no operator choosing
a URL's wire. A 2026-07-28 session that sends `_meta` on every request lands on stateless, the
same as dual-mode HTTP, under the rule below. Before #1637 a stateless client over stdio got `-32600 server not
initialized`, and an adaptive one failed `Connect` on the probe.

**In dual mode `_meta` wins over the session id** (#1628). `detectWireKind` checks
`_meta.protocolVersion` (signal 3) before `Mcp-Session-Id` (signal 4), so a client that ran
`initialize` at 2026-07-28 and then sends the required `_meta` on every call is served by the
stateless dispatcher, and its session caps and GET SSE push go unused. We kept it that way on
purpose. 2026-07-28 drops `initialize`, so a session at that version is back-compat only, and
push on 2026-07-28 is `subscriptions/listen`. Reordering would have meant a session lookup inside
detection plus a Go client option to negotiate 2026-07-28 in `initialize` (it always sends
2025-11-25), for a client shape the spec is retiring. A deployment that really needs the session
served picks `ModeLegacyOnly`, which is what `parityWires` does. Revisit if someone needs push
on a 2026-07-28 session. #1625 builds on this: in dual mode the session arm of a shared handler
only ever sees traffic without `_meta`.

**A notification never gets a JSON-RPC reply, on any transport** (#1520). The handler runs and
the reply is dropped, in the session `Dispatch`, in `dispatchStateless` and on stdio, so HTTP
answers `202` with no body. A refused notification on the stateless wire (a removed method, a bad
envelope) gets only its HTTP status, `404` or `400`. "Notification" means `req.IsNotification()`,
which counts `"id":null` as well as a missing `id`; a raw `ID == nil` check misses the first,
which is how stdio kept answering `"id":null` after #1639 and how a panicking `"id":null`
notification got a `-32603`. Notifications also skip the session's in-flight tracking, keyed by
id, where every one of them shared the key `null` and a second concurrent one was refused as a
duplicate. The client sends notifications without an `id` member since the same change.

**An adaptive probe has three ways to hear "no stateless wire here"**: `-32601` in a 200, HTTP
404, or HTTP 400 with a body that is not a JSON-RPC error. The last is what go-sdk, the
TypeScript SDK and mcpkit's own legacy-only transport (before #1637) send for a request without
`Mcp-Session-Id`, since they check the header before the method. A 400 carrying a JSON-RPC error
(`-32020`, `-32022`) is a stateless server refusing the probe, and stays fatal.

---

## Handler return ABI is a sealed interface (#486, PR 487)

`ToolHandler` returns `(core.ToolResponse, error)`; `PromptHandler` returns
`(core.PromptResponse, error)`.

Concrete `ToolResponse` variants: `ToolResult` (sync), `InputRequiredResult` (MRTR),
`CreateTaskResult` (SEP-2663 task envelope), `GoAsyncResult` (in-process spawn signal).

`core.ToolResult` no longer carries `IsInputRequired` / `InputRequests` / `GoAsync` sentinel
fields. They live on dedicated variant types. `ctx.RequestInput` returns
`(core.InputRequiredResult, error)`.

Handler bodies mostly do not change, and `return core.ToolResult{...}, nil` still compiles. Use
`core.TypedTool[X, core.ToolResponse]` for handlers returning polymorphic variants. Migration
recipe: `docs/HANDLER_RETURNS_MIGRATION.md`.

---

## Reverse-call restrictions on the stateless wire

**`ctx.Sample` and `ctx.Elicit` are forbidden on the stateless wire**, since server-initiated push does
not exist there. The legacy push API errors with `ErrNoRequestFunc` on stateless requests by
construction.

Tool handlers route through MRTR instead, via `core.NewSamplingInputRequest` /
`core.NewElicitationInputRequest` plus the matching decoders. The godocs on Sample and Elicit
spell out the migration with worked examples.

---

## `subscriptions/listen` filters are a registry, not a struct (#1504)

The 2026-07-28 `SubscriptionFilter` names three base keys, and every extension that delivers a
notification over `subscriptions/listen` adds one of its own: `eventsListChanged` (SEP-3415),
`agentsListChanged` (agents-wg#22), `taskIds` (ext-tasks). Until #1504 the filter was a closed
struct and `Matches` returned false for anything else, so no extension notification could ever
reach a stateless listener, and nothing failed to say so. Now an extension calls
`srv.RegisterSubscriptionFilter`, and the listen handler resolves the request key by key.

The hook takes raw JSON and returns a matcher rather than taking a bool, because `taskIds` is an
array that has to be narrowed per caller (authorization runs in `Accept`, on the request context)
and matched against each notification's params. A bool-only seam would have covered events and
agents and then needed a second mechanism for Tasks. The TypeScript SDK's `listenRouter.ts` keeps
the filter closed, so there is no reference design for this part.

Three things the same PR fixed, all of them 2026-07-28 schema rules the upstream suite does not
grade:

- **The `subscriptionId` is the listen request's JSON-RPC id.** mcpkit minted a `sub-...` string
  instead. The suite only checks that frames carry *a* matching id, so it passed. The minted value
  survives as the internal map key, since two clients can both use id 1.
- **The acknowledgement carries the honored filter.** A base key counts as honored only when the
  matching `listChanged` capability is advertised. The schema's own example is
  `promptsListChanged` on a server with no prompts. mcpkit derives that capability from what is
  registered, so a stateless server with no prompts at listen time declines the key, and a client
  that wants prompt changes later has to listen again.
- **`NewTaggedFrame` nested struct params under `"data"`** and dropped any `_meta` the
  notification already carried. Harmless while only bare list-changed notifications went out, wrong
  for every typed extension notification. It now flattens anything that marshals to an object.

Not done: `resourceSubscriptions` and the graceful-close `SubscriptionsListenResult`, both in the
same schema section. See the follow-up issues linked from #1504.

---

## SEP-2549 `ttlMs` is two-state client-side

The merged spec treats an absent `ttlMs` the same as `0` (both "immediately stale"). mcpkit still
types it `*int` so a server can emit an explicit `ttlMs: 0` distinct from omitting the field.
Plain `int` plus omitempty cannot express that.

`cacheScope` is a plain `string` with omitempty; absent defaults to `"public"` client-side.

The field was renamed from `ttl` (seconds) during the spec's final review. See
`docs/LIST_TTL_MIGRATION.md`.

---

## `HandleStore[T]` is opt-in scaffolding, not a contract

SEP-2567 is design guidance only, with no wire contract and no upstream conformance. Any storage a tool
handler can call (Redis, SQL, `sync.Map`, custom RPC) satisfies the pattern.

`server.HandleStore[T]` ships the typed in-memory default plus the interface seam. Use it, replace
it, or skip it, and all three are equally SEP-2567-compliant. See `docs/SEP_2567_HANDLES.md`.

---

## SEP-2577 deprecations: annotated, not removed

Roots, Sampling, and Logging surfaces carry `// Deprecated:` blocks pointing at
`docs/SEP_2577_DEPRECATIONS.md`. **They keep working, with no behavior change.** Only
`staticcheck SA1019` warnings fire at call sites.

Removal is deferred to a future release (~2027, no earlier than the spec dropping them plus the
12-month annotation window closing ~2027-05-21), tracked in #850. Removing them early would break
the 12-month promise *and* drop mcpkit below 100% conformance and Tier 1, since the tier doc counts
sampling and elicitation.

Affected public symbols are enumerated in `docs/SEP_2577_DEPRECATIONS.md`.

---

## Stdio framing: both ends were wrong the same way (#1517)

MCP stdio is newline-delimited JSON, one message per line. Until #1517 both `RunIO` and the client's
`StdioTransport` spoke LSP-style `Content-Length` framing instead, so an mcpkit stdio server never
answered Claude Code, Cursor, or the Python and TS SDKs. It hung in `ReadFrame`, reading the first
JSON line as a header and waiting for the blank line that ends the headers. Every stdio test was
green throughout because every one paired an mcpkit client with an mcpkit server, and a framing both
ends share round-trips fine.

- Framing lives in `internal/stdioframe`, shared by both sides. `server` and `client` re-export the
  type as `StdioFraming`.
- The server defaults to `FramingAuto`: the first non-whitespace byte picks the framing (`{` or `[`
  means newline-delimited, anything else `Content-Length`), and replies follow it for the rest of the
  session. Old mcpkit clients keep working.
- The client cannot detect anything, because it speaks first. It defaults to newline-delimited, so a
  new client against an mcpkit server built before #1517 hangs at `initialize` unless it passes
  `FramingContentLength`.
- Newline-delimited reads use `bufio.Reader.ReadBytes`, not `bufio.Scanner`, whose 64 KiB token
  limit would cut off a large tool call. Writes compact any message containing a line break, since
  `stdioPush` forwards raw bytes that never went through `json.Marshal`.
- The check that doesn't share our code is `tests/e2e/stdio_interop_test.go` (opt-in, pulls
  `@modelcontextprotocol/server-everything` from npm): `MCPKIT_STDIO_INTEROP=1 go test -run
  StdioInterop` in `tests/e2e`. Run it after touching either stdio transport.

---

## Client and server gaps found by the room bridge (#1585)

Building `experimental/ext/rooms` (#1584) turned up four places where an extension had to work
around the library. Two were real and two were already solved, just hard to find.

`OnSessionClose` exists because the bridge kept pairings in memory until a page left, with an
exported `EndSession` for callers to remember to call. Every close path already ended in
`Dispatcher.Close` (DELETE, idle expiry, `CloseSession`, `CloseAllSessions`, the SSE disconnect
paths in `transport.go`, stdio and in-process), so the hook lives there behind a `sync.Once`, with
one hook list shared by every session dispatcher the way `subManager` is. A hook registered after a
session started still fires for it.

The client never sent `notifications/cancelled`. On Streamable HTTP that mostly did not matter,
because aborting the POST cancels the handler, but the in-process and stdio paths went through
`coreTransportAdapter`, which handed the transport `context.Background()`, and the legacy SSE
transport built its POST without the context and waited on the response channel with no ctx case.
So a cancelled call over any of those kept waiting for the tool to finish. All three take the
caller's context now. The notification is skipped on the stateless wire, which removed it from the
method set (`removedStatelessMethods` in `server/stateless_dispatch.go`).

The other two looked like gaps and were not. A 403 on a static token does keep its scopes: they are
on `*client.ClientAuthError` (servicekit's `AuthRetryError`) as `RequiredScopes`, and the flat
"does not support step-up" string is only its `Cause`. The rooms PR read the message and missed the
type. And an extension never needed `ext/auth` to send a scope challenge, since `*core.AuthError`
with `core.InsufficientScopeChallenge` is what `ext/auth` itself returns; `core.InsufficientScope`
and `(*ScopeChallenge).AuthError` just stop each caller rebuilding it.

## Testing

- **The server requires initialization.** A direct `srv.Dispatch()` in a test fails. Use httptest
  plus a client.
- Version negotiation, feature gating, and duplicate-initialize behavior all have tests keyed to
  `protocol_features.go`; add there rather than inline.
- **Compare context errors with `errors.Is`, never `==`, once an HTTP client is in the path.**
  `TestWaitForTaskTimeout` asserted `err != context.DeadlineExceeded` and flaked in CI.
  `WaitForTaskV1` polls on an interval against a deadline, so which error surfaces is a race: expire
  between polls and you get `ctx.Err()` bare, expire while a POST is in flight and `http.Client`
  returns a `*url.Error` wrapping it. Identity comparison passed only in the first case.
  `-count=60 -cpu=1` reproduces it; at default parallelism it hid through 40 clean runs.
  `ext/tasks/client_integration_test.go` had the same shape on `context.Canceled`. Identity is still
  fine where nothing can wrap the error — a goroutine handing back `ctx.Err()` over a channel, or
  `core/detach_test.go` comparing `ctx.Err()` directly.
