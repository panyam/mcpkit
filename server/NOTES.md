# server/ — implementation notes

Dispatch and protocol lore. For the public API see `README.md`; for enforceable rules see
`CONSTRAINTS.md`.

---

## Version negotiation and version-gated features

**`server/protocol_features.go` is the single source of truth.** Do not scatter fresh
`negotiatedVersion == "..."` checks anywhere else — that is what let the legacy and stateless
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

The stateless wire (`server/stateless/dispatch.go`) still has its own version-error path. Fold it
through the resolver when #493 collapses the package.

---

## Stateless-wire dispatch parity — the recurring failure mode

**`server/stateless_backend.go::callToolForStateless` must mirror every pre-handler step
`Dispatcher.handleToolsCall` runs**, or features silently no-op on the SEP-2575 wire.

This has bitten repeatedly. A dispatch feature added to the legacy path but not to
`callToolForStateless` works on legacy and fails silently on stateless, and conformance often
misses it because the task and file-input suites run legacy while the stateless suite uses the
cart fixture.

Three landed this way:
- MRTR (decodes the full envelope: `inputResponses` plus `requestState`, verifies, merges, re-mints)
- SEP-2356 file-input validation (`validateFileInputArgs` before the handler)
- client-side SEP-2243 `Mcp-Name` routing header for task ops

`make verify-dual` (#478) drives example walkthroughs over both wires to catch this class; see
`examples/DUAL_MODE_AUDIT.md`.

**Still uninstrumented**: stateless `resources/read` and `completion/complete` do not traverse the
middleware chain at all, so auth middleware cannot gate them on the stateless wire yet. That is a
separate, broader gap.

### Typed middleware errors on the stateless wire (#815, PR 816)

A `server.Middleware` that short-circuits with `*core.AuthError` (SEP-2350 scope challenge, step-up
auth) now reaches the transport's shared `writeAuthError`, emitting HTTP 403 plus
`WWW-Authenticate`. Previously it was folded into a generic `-32603` / HTTP 200, losing both.

Mechanism: `stateless.Backend.InvokeWithMiddleware` returns `(*core.Response, error, bool)` and
`stateless.Dispatcher.Dispatch` returns `(*core.Response, error)`. The three middleware-bearing
branches forward the raw error; the rest wrap with `nil`. `handleStatelessPost` and
`handleStatelessPostSSE` call `writeAuthError` on a non-nil dispatch error. The SSE path is safe
because middleware short-circuits before any frame is written, so `sseStarted` is always false.

**Behavior-parity note**: this routes *all* stateless middleware errors through `writeAuthError`,
so a non-`AuthError` middleware error now maps to HTTP 401 (matching the legacy wire) instead of
`-32603` / HTTP 200.

---

## Wire mode defaults are deliberately asymmetric

- Server: `stateless.DefaultMode = stateless.ModeDual`. Additive on upgrade — every existing
  server gains the stateless wire on one URL.
- Client: `client.DefaultClientMode = client.ClientModeLegacyOnly`. Conservative — `Adaptive`
  would have silently broken 11 pre-existing client tests that assume the legacy initialize
  handshake.

Override per deployment via constructor option (`server.WithStatelessMode(...)` /
`client.WithClientMode(...)`), env var (`MCPKIT_STATELESS_MODE` / `MCPKIT_CLIENT_MODE`), or an
`init()` flip of the package var. The shipping client default may flip to `Adaptive` in a future
major release; the doc block spells out the migration.

This asymmetry was empirically re-confirmed by the client conformance harness: a blanket
`ClientModeAdaptive` breaks legacy mocks in the same suite.

---

## Handler return ABI is a sealed interface (#486, PR 487)

`ToolHandler` returns `(core.ToolResponse, error)`; `PromptHandler` returns
`(core.PromptResponse, error)`.

Concrete `ToolResponse` variants: `ToolResult` (sync), `InputRequiredResult` (MRTR),
`CreateTaskResult` (SEP-2663 task envelope), `GoAsyncResult` (in-process spawn signal).

`core.ToolResult` no longer carries `IsInputRequired` / `InputRequests` / `GoAsync` sentinel
fields — they live on dedicated variant types. `ctx.RequestInput` returns
`(core.InputRequiredResult, error)`.

Handler bodies usually do not change: `return core.ToolResult{...}, nil` still compiles. Use
`core.TypedTool[X, core.ToolResponse]` for handlers returning polymorphic variants. Migration
recipe: `docs/HANDLER_RETURNS_MIGRATION.md`.

---

## Reverse-call restrictions on the stateless wire

**`ctx.Sample` and `ctx.Elicit` are forbidden on the stateless wire** — server-initiated push does
not exist there. The legacy push API errors with `ErrNoRequestFunc` on stateless requests by
construction.

Tool handlers route through MRTR instead, via `core.NewSamplingInputRequest` /
`core.NewElicitationInputRequest` plus the matching decoders. The godocs on Sample and Elicit
spell out the migration with worked examples.

---

## SEP-2549 `ttlMs` is two-state client-side

The merged spec treats an absent `ttlMs` the same as `0` (both "immediately stale"). mcpkit still
types it `*int` so a server can emit an explicit `ttlMs: 0` distinct from omitting the field —
plain `int` plus omitempty cannot express that.

`cacheScope` is a plain `string` with omitempty; absent defaults to `"public"` client-side.

The field was renamed from `ttl` (seconds) during the spec's final review. See
`docs/LIST_TTL_MIGRATION.md`.

---

## `HandleStore[T]` is opt-in scaffolding, not a contract

SEP-2567 is design guidance only: no wire contract, no upstream conformance. Any storage a tool
handler can call (Redis, SQL, `sync.Map`, custom RPC) satisfies the pattern.

`server.HandleStore[T]` ships the typed in-memory default plus the interface seam. Use it, replace
it, or skip it — all three are equally SEP-2567-compliant. See `docs/SEP_2567_HANDLES.md`.

---

## SEP-2577 deprecations: annotated, not removed

Roots, Sampling, and Logging surfaces carry `// Deprecated:` blocks pointing at
`docs/SEP_2577_DEPRECATIONS.md`. **They keep working — no behavior change.** Only
`staticcheck SA1019` warnings fire at call sites.

Removal is deferred to a future release (~2027, no earlier than the spec dropping them plus the
12-month annotation window closing ~2027-05-21), tracked in #850. Removing them early would break
the 12-month promise *and* drop mcpkit below 100% conformance and Tier 1, since the tier doc counts
sampling and elicitation.

Affected public symbols are enumerated in `docs/SEP_2577_DEPRECATIONS.md`.

---

## Testing

- **The server requires initialization.** A direct `srv.Dispatch()` in a test fails. Use httptest
  plus a client.
- Version negotiation, feature gating, and duplicate-initialize behavior all have tests keyed to
  `protocol_features.go`; add there rather than inline.
