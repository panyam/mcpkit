# Events Spec Alignment — Phase 2 Plan

**Status:** Drafted 2026-05-01. α + β shipped as of 2026-05-02. Replaces the spec-tracking work that landed in mcpkit#323 (phase 1).
**Scope:** `experimental/ext/events/` library, `experimental/ext/events/clients/go/` SDK, the discord + telegram demos under `examples/events/`.
**Spec target:** the MCP Events extension spec sketch cached at [`proposals/triggers-events-wg/spec-snapshots/design-sketch-2026-04-30.md`](https://github.com/panyam/mcpcontribs/blob/main/proposals/triggers-events-wg/spec-snapshots/design-sketch-2026-04-30.md) (in mcpcontribs, not this repo). Substantial revisions landed in the upstream sketch in late April that broke from the direction phase 1 implemented; this plan re-aligns.

**Citation convention:** every delta in the per-PR sections below carries a citation in the form `(§"Section name" L<line>)` referencing the cached snapshot. Line numbers are stable against that snapshot file; if a future spec revision lands, we add a new dated snapshot rather than rewriting old citations. PR descriptions and commit messages following this plan should carry the same form so reviewers can correlate code → spec text directly.

## TL;DR

Seven independent PR groups (α–η), best landed in dependency order. None individually large; ε is the largest. None are gratuitous refactors — every item maps to a specific delta between today's wire / API and the latest spec.

| Group | Theme | Rough size | Risk | Why this position |
|---|---|---|---|---|
| **α** | Wire renames + error code remap | ~30 LOC across 5 files | None | Mechanical; reviewers see the spec's vocabulary in our handlers. Unblocks β. |
| **β** | Collapse webhook secret modes to client-supplied | **~150 LOC deleted** | Low | Spec actually got *simpler* than us. Single-purpose deletion PR. |
| **γ** | Auth + tuple subscription identity | Larger; touches webhook critical path | Medium | Depends on `ext/auth` plumbing. May feature-gate. |
| **δ** | Request shape alignment (flat poll, `maxAge`, EventOccurrence audit) | Bounded | Low | Wire-breaking but spec is firm. |
| **ε** | Push delivery (`events/stream` + notifications surface) | Largest | Medium | Worth a separate design PR before code. Borrow schema shapes from the TS reference SDK. |
| **ζ** | Webhook hardening (delivery-time SSRF, body cap, control envelopes, deliveryStatus) | Medium | Low | Defense-in-depth + spec compliance. |
| **η** | SDK hooks (match/transform, on_subscribe, poll-lease) | Larger | Medium | More design needed; defer until ε settles. |

The full delta map (50 spec items × current state) lives in the proposals working notes; this doc carries the implementation-facing plan.

## Why now

Phase 1 (mcpkit#323) tracked the spec as it stood through April 24-29. On April 30 the spec sketch had a major revision that:

1. **Simplified** the webhook secret model — collapsed three modes (`Server` / `Client` / `Identity`) into one (`Client`-supplied, REQUIRED, `whsec_` + 24-64 base64 bytes)
2. **Tightened** subscription identity — adds `principal` to the tuple and removes the client-supplied `id`; server derives the routing handle deterministically from the tuple
3. **Added** new wire surface — `notifications/events/heartbeat` (with cursor), `notifications/events/active`, `notifications/events/error` (transient), `notifications/events/terminated`, webhook control envelopes (`type:gap`, `type:terminated`)
4. **Renamed** existing concepts — `cursorGap` → `truncated`; error codes moved to `-32011..-32017`; `webhook-id` for event deliveries SHOULD equal `eventId`
5. **Added** `maxAge` replay floor across all three delivery modes
6. **Mandated** Standard Webhooks signature scheme as the only profile (we already did this in phase 1, PR #336 — keeps us ahead)

Some of phase 1's work is now strictly unnecessary (the Identity-mode crypto). Some needs renaming. Some needs net-new building. This plan separates the buckets so each PR has one job.

## Approach

### Principles

1. **One delta per PR** — don't bundle a rename with a new feature. Reviewers scanning the diff should see one spec change.
2. **Wire-level commits, not refactor commits** — every commit message names the spec section / line / rationale. We make the relationship to the spec the audit trail.
3. **Deprecate-then-delete for spec-tracking removals** — same policy as phase 1 (see "Deprecation policy" below). The spec can flip; we want a clean revert path while it stabilizes.
4. **Don't delegate understanding** — every PR description names the file paths it touches and the before/after wire snippet. No "see plan" without explanation.
5. **Independent PRs** — each group is mergeable on its own, even if later groups never land. Avoid coupling β's work to γ's auth plumbing, etc.

### What's out of scope

- **Push delivery via the existing `Server.Broadcast` path** — keep it working until ε lands real `events/stream`. Don't gut it preemptively.
- **Cross-repo coordination with TS SDK** — they'll need similar changes; we're not waiting for them. Our changes are independently valid against the spec.
- **CONSTRAINTS.md additions** — none of this is foundational architecture. Constraints get added if a *recurring* pattern emerges across PRs.
- **Performance work** — e.g., zero-copy delivery paths. These are speculative until profiles say otherwise.

### Open spec questions we're NOT chasing yet

The spec sketch lists six explicit Open Questions. Of those, only one bears on our impl:

- **OQ6 — endpoint verification challenge** (`{type:verification}` envelope). We won't implement this until the spec resolves. ζ adds the other two control envelopes (`type:gap`, `type:terminated`) but leaves verification as a TODO marker.

The others (resource-template subsumption, MCP task event subsumption, event-name namespacing, ordering/consistency strengthening, server card egress publication) don't touch our impl directly.

## PR groups

### α — Wire renames + error code remap

**Status:** merged via PR #353.

**Goal:** make our wire bytes use the spec's vocabulary without changing semantics.

**Spec deltas addressed:**
- `cursorGap` → `truncated` field on poll response, push `notifications/events/active`, webhook subscribe response
- `events/poll` response wrapper: drop `results: [{...}]` array, lift the single entry's fields to top level (since batching was already removed in phase 1, the wrapper is leftover)
- Error codes: `-32001 EventNotFound` → `-32011`; `-32005 InvalidCallbackUrl` → `-32015`; add the missing codes (`-32012` Unauthorized, `-32013` TooManySubscriptions, `-32016` SubscriptionNotFound, `-32017` DeliveryModeUnsupported)
- `webhook-id` header on event deliveries: use `eventId` (today we generate `msg_<random>`). Control envelopes — once they exist in ζ — use `msg_<type>_<random>`.

**Files touched:**
- `experimental/ext/events/events.go` — `pollResultWire` struct, the `events/poll` handler, `events/subscribe` error returns, the `EventNotFound` paths
- `experimental/ext/events/yield.go` — `PollResult.CursorGap` → `Truncated` rename, all callers
- `experimental/ext/events/headers.go` — `newMessageID` becomes `newControlMessageID`; event-delivery path uses `eventId` directly
- `experimental/ext/events/README.md` — flag and section renames
- `experimental/ext/events/clients/go/event.go` + `subscription.go` — typed accessor renames if any leak (`HasCursorGap` → `Truncated`)
- `experimental/ext/events/clients/python/events_client.py` — same rename in poll-result decode

**Acceptance:**
- `make test-experimental` green with renamed fields throughout
- Wire bytes for an `events/poll` response contain `"truncated"` not `"cursorGap"`, no `"results"` wrapper
- Wire bytes for a webhook delivery contain `"webhook-id": "<eventId>"`
- Error codes audit: `grep -rn "-3200[0-9]" experimental/ext/events/` returns zero matches in the events code (only valid range is `-32011..-32017`)

**Tests:**
- Update `headers_test.go` to assert the new `webhook-id` shape on event deliveries vs control envelopes (control envelopes added in ζ, but the header generator should already differentiate)
- New test: `events/poll` JSON shape end-to-end matches the spec's example response
- New test: every JSON-RPC error response from the events handlers carries a code in `-32011..-32017`

**Risk:** None — pure wire renames, no new behavior. Only landmine is callers in tests / examples that grep for old field names.

**Deprecation policy:** Not applicable — these are renames of internal symbols and wire fields, not removals of functionality.

---

### β — Collapse webhook secret modes to client-supplied only

**Status:** merged via PR #355.

**Goal:** match the spec's client-supplied-only secret model. The server no longer generates secrets; the client always provides the signing key on subscribe. Net-deletion PR.

**Spec deltas addressed:**
- `delivery.secret` is REQUIRED on `events/subscribe`, MUST be `whsec_` + base64 of 24-64 random bytes; servers MUST reject anything else with `-32602 InvalidParams`
- Subscribe response no longer carries a `secret` field (server doesn't generate one)
- `events/unsubscribe` resolves the subscription via the tuple identity (handled in γ); the secret-form unsubscribe path (`UnregisterBySecret` with constant-time compare) is no longer needed

**Files touched:**
- `experimental/ext/events/secret.go` — DELETE the `WebhookSecretMode` enum, `WithWebhookSecretMode`, `WithWebhookRoot`, `deriveIdentitySecret`, `deriveIdentityID`, `canonicalTuple`, `ParseSecretMode`, `WebhookSecretServer`, `WebhookSecretClient`, `WebhookSecretIdentity`. Keep `generateSecret` only for SDK-side client convenience (it generates a `whsec_` value the *client* can pass).
- `experimental/ext/events/webhook.go` — drop `secretMode`, `root`, `resolveSecret`. Subscribe handler validates the supplied secret format and stores it as-is. Drop `UnregisterBySecret`.
- `experimental/ext/events/events.go` — `events/subscribe` handler: validate `delivery.secret` matches `whsec_<24-64-bytes-base64>`, reject otherwise with `-32602`. Drop `secret` from response. Drop the `params` field input (that's an Identity-mode artifact).
- `experimental/ext/events/secret_test.go` — DELETE the identity-mode tests; keep a minimal test for the validator helper
- `experimental/ext/events/registry_modes_test.go` — DELETE most of it; keep only what tests the client-supplied happy path
- `experimental/ext/events/README.md` — drop the secret-mode comparison section; document the validator
- `experimental/ext/events/DEPLOYMENT.md` — drop the Identity-mode "treat root like a master credential" section
- Discord + telegram demos — drop `--webhook-secret-mode` and `--webhook-root` flags from `main.go`; update their READMEs
- Go SDK `experimental/ext/events/clients/go/subscription.go` — Subscribe always supplies a `whsec_` value (auto-generate if `Secret` option is empty); response no longer carries one back; `Subscription.Secret()` returns the value the SDK supplied
- Python SDK `experimental/ext/events/clients/python/events_client.py` — same change

**Acceptance:**
- `make test-experimental` green
- `experimental/ext/events/` LOC count drops by ~150 net
- A subscribe with no secret returns `-32602 InvalidParams: delivery.secret is required`
- A subscribe with `secret: "not-a-real-secret"` returns `-32602 InvalidParams: delivery.secret must be whsec_<base64 of 24-64 bytes>`
- A subscribe with a valid `whsec_` value succeeds and the response does NOT contain a `secret` field

**Tests:**
- Deletion of identity-mode tests is itself part of the diff; reviewer can audit the test count delta
- New positive test: SDK auto-generates a valid secret when `Secret` is empty
- New negative tests for the two validator failure modes (missing secret, malformed secret)

**Risk:** Low. Only callers of the deleted symbols are our own tests + demos. Outside callers (other repos importing `experimental/ext/events`) would break, but `experimental/` is documented as unstable.

**Deprecation policy:** Not applicable for the deleted modes — the spec mandated their removal, and `experimental/` carries no stability promise. We delete, not deprecate.

---

### γ — Auth + tuple subscription identity

**Status:** merged via PR #356.

**Goal:** key webhook subscriptions on `(principal, delivery.url, name, params)` per the spec; reject unauthenticated webhook subscribe / unsubscribe.

**Spec deltas addressed:**
- `events/subscribe` and `events/unsubscribe` MUST require an authenticated principal (`-32012 Unauthorized` if missing) (§"Subscription Identity" → "Authentication required" L361, error code table L110)
- Subscription key is `(principal, delivery.url, name, params)`. `params` compared by canonical-JSON equality. All four components are immutable for the subscription lifetime. (§"Subscription Identity" → "Key composition" L363)
- No client-generated `id`. Server derives a deterministic SHA-256 over the canonical key serialization. (§"Subscription Identity" → "Derived `id`" L367)
- The derived `id` is a routing handle, not a capability. Appears in `X-MCP-Subscription-Id` on every webhook delivery POST (header lives in §"Webhook Event Delivery" L390 — γ produces the value, ζ wires the header).
- `events/unsubscribe` accepts only `(name, params, delivery.url)` from the client; `principal` is taken from the auth context. Rejects an `id`-keyed unsubscribe. (§"Unsubscribing: events/unsubscribe" L509)
- Cross-tenant isolation property: two distinct tenants subscribing to the same `(name, params)` get distinct subscriptions; learning another tenant's derived `id` gains nothing. (§"Subscription Identity" → "Cross-tenant isolation" L378)

**Cross-PR note:** `X-MCP-Subscription-Id` header emission technically belongs in ζ (it's a delivery-time concern), but γ produces the `id` value. ζ wires the header onto the outbound POST.

**Files touched:**
- `experimental/ext/events/webhook.go` — registry rekeyed on the canonical tuple. `Register` takes `(principal, url, name, params, secret)` and returns the derived `id`. New `canonicalKey(principal, url, name, params) []byte` helper. New `deriveSubscriptionID(canonicalKey) string`.
- `experimental/ext/events/events.go` — `events/subscribe` reads principal from the method context (see auth integration below), validates presence, computes the derived id, returns it. `events/unsubscribe` resolves by tuple. Drops the `id` input field. Drops the `secret`-form unsubscribe path entirely.
- `experimental/ext/events/clients/go/subscription.go` — subscribe takes `Name` + `Params` + `URL` + `Secret` and gets back the derived id. Drops `SubID` option. `Subscription.ID()` returns the server-derived value.
- Python SDK — same shape change
- Discord + telegram demos — drop `SubID` arg in their subscribe calls (the only callers); rely on the server-derived id
- New file: `experimental/ext/events/identity.go` — pure-functional helpers `canonicalKey`, `deriveSubscriptionID`. Easy to unit-test.

**Auth integration:**
- The events handler reads the principal via `core.MethodContext.AuthClaims()` returning `*core.Claims` (mcpkit core, not `ext/auth`). `claims.Subject` is the principal in the canonical tuple.
- For unauthenticated mcpkit servers: webhook subscribe MUST return `-32012`. Poll and push are unaffected (they don't require auth per the spec).
- For the discord + telegram demos: hybrid auto-detect via `OAUTH_ISSUER` env var. When set, wire `server.WithAuth(JWTValidator)` and follow spec strictly. When not set, fall back to `events.Config.UnsafeAnonymousPrincipal: "demo-user"` so `make demo` runs end-to-end without an OIDC provider. Server logs which posture is active at startup.

**Composition note (WG-relevant):** `ext/events` has zero compile-time dependency on `ext/auth`. It depends on `core.Claims` (the abstract auth contract). Any auth provider that populates `core.Claims` works — JWT/OIDC via `ext/auth`, mTLS-derived principals, session cookies, custom validators. Auth and Events are independent extensions composed at the wiring layer (`server.WithAuth(...)` in `main.go`), not at the protocol-implementation layer. This is the right shape for MCP extensions: extensions depend on stable core abstractions, not on each other.

**Acceptance:**
- `make test-experimental` green
- `events/subscribe` from an unauthenticated client returns `-32012 Unauthorized`
- Two subscribes with the same `(name, params, url)` from the same authenticated principal return the same derived `id`; second call is a TTL refresh, not a new subscription
- Two subscribes with same tuple from *different* principals get *different* derived `id`s (cross-tenant isolation test)
- `events/unsubscribe` with a payload containing `id` is rejected; with the tuple shape it succeeds

**Tests:**
- Unit tests for `canonicalKey` (canonical-JSON, key ordering invariance)
- Unit tests for `deriveSubscriptionID` (deterministic; collision-resistant for typical inputs)
- Integration test for the auth-required rejection
- Integration test for the cross-tenant isolation property

**Risk:** Medium. Changes the wire shape of subscribe/unsubscribe. Anyone consuming `experimental/ext/events` from outside our repo gets a breaking change — acceptable because `experimental/`.

**Deprecation policy:** Not applicable — the subscription identity tuple is the spec, not an extension. We need the spec shape to be the only shape we accept. Old shape gets deleted, not deprecated.

---

### δ — Request shape alignment (flat poll, maxAge, EventOccurrence audit)

**Status:** in flight on `feat/events-delta-wire-shapes`; 5 commits landed on the branch, PR pending.

**Goal:** make `events/poll` and `events/subscribe` request bodies match the spec's flat shape; add `maxAge` floor; audit EventOccurrence fields.

**Spec deltas addressed:**
- `events/poll` request: drop `subscriptions[]` array (already single-entry-enforced after phase 1). Lift `name`, `params`, `cursor` to top level. Add optional `maxAge` (integer seconds), `maxEvents` (integer). (§"Poll-Based Delivery" → "Request: events/poll" L139-149)
- All three modes (poll, stream, subscribe) accept optional `maxAge`. Server begins replay from `max(cursor, now - maxAge)`. (§"Cursor Lifecycle" → "Bounding replay with maxAge" L529)
- `EventOccurrence` field set: `eventId` (string, required), `name` (string, required), `timestamp` (ISO 8601, required), `data` (object, required), `cursor` (string | null, optional), `_meta` (object, optional). Confirm we emit the required five with the right names; fix any drift. (§"EventOccurrence schema" L180-186; `_meta` added in spec follow-on commit `d4faef9` 2026-05-01)
- `events/list` response: add optional `nextCursor` for pagination consistency with base MCP list endpoints (`tools/list`, `resources/list`). Today the events list is small enough that we'd always omit it; thread the field through and emit only when set. (Spec follow-on commit `d4faef9` 2026-05-01 — pre-cached snapshot doesn't show this section yet; future snapshot will.)
- `EventDescriptor` (per event-type definition in `events/list`): add optional `_meta` field, matching the metadata pattern on `Tool` / `Resource` / `Prompt`. Authors can attach arbitrary metadata; the SDK threads it through. (Spec follow-on commit `d4faef9` 2026-05-01)

The `nextCursor` and the two `_meta` fields are spec follow-ons added on 2026-05-01 (after the in-tree plan's first draft). Folded into δ rather than spinning a micro-PR because δ is already the "wire-shape audit" PR — these three are the same kind of work.

**Files touched (as built):**
- `experimental/ext/events/events.go` — `registerPoll` flattens the request struct; `registerSubscribe` adds `MaxAge int`; new `listResultWire` typed struct gives `events/list` a `nextCursor` field with `omitempty`. `Event` and `EventDef` gain `Meta map[string]any` with JSON tag `_meta,omitempty`.
- `experimental/ext/events/webhook.go` — `WebhookTarget` gains `MaxAgeSeconds int`; `Register` signature grows a `maxAgeSeconds` parameter (refresh treats `0` as "don't change", non-zero replaces stored floor).
- `experimental/ext/events/clients/go/subscription.go` — `SubscribeOptions.MaxAge time.Duration`, sent on every subscribe + refresh.
- `experimental/ext/events/clients/go/event.go` + `receiver.go` — `Event[Data].Meta map[string]any`, threaded through the receiver decoder.
- `experimental/ext/events/clients/python/events_client.py` — `WebhookSubscription(max_age=0)` kwarg; `--max-age` flag on `webhook` and `poll` subcommands; receiver + cmd_poll printout surface `_meta`; `cmd_list` loops on `nextCursor`.
- `experimental/ext/events/yield.go` — `YieldingSource.SetMetaFunc(func(Data) map[string]any)`. Typed setter (not a YieldingOption — option-functions don't compose with the generic `Data` type) so authors can derive per-event `_meta` without touching the yield closure signature.
- Discord + telegram demos — every `events/poll` call site flattened. Both walkthroughs pass `MaxAge: 5*time.Minute` on subscribe (δ-3 plumbing). Discord additionally uses `SetMetaFunc` to derive `channel_type` + `mention_count` per event and `EventDef.Meta = {"category": "messaging"}` for type-level metadata.

**Decision:** kept `EventSource.Poll(cursor, limit)` interface unchanged. `maxAge` filtering happens at the handler layer in `registerPoll`, after `source.Poll()` returns: drop events with `timestamp < now - maxAge`, set `truncated: true` if any were dropped, advance `cursor` to source head when filtering removed everything (so the client doesn't re-poll for the dropped events). Avoids forcing every `EventSource` implementor to learn about replay floors. Sources with non-RFC3339 timestamps are conservatively kept.

**Acceptance:**
- `make test-experimental` green
- Wire-shape test: an `events/poll` request body with `"name"` at top level (no `subscriptions[]` wrapper) is accepted
- `maxAge` floor test: subscribe with `maxAge: 1` after sleeping 2s past a known event, verify the event is dropped from replay
- `EventOccurrence` field audit: every wire-format event has the five required fields with the right names

**Tests (as built):** added to `experimental/ext/events/wire_shape_test.go` —
- `TestPoll_FlatRequestShape`, `TestPoll_RejectsLegacyWrapper` (δ-1)
- `TestPoll_MaxAgeFiltersOldEvents`, `TestPoll_MaxAgeZeroMeansNoFilter` (δ-2)
- `TestSubscribe_AcceptsMaxAge`, `TestSubscribe_DefaultsMaxAgeToZero` (δ-3)
- `TestEvent_MetaSerializesWithUnderscorePrefix` + omits-when-absent counter; same pair for `EventDef` (δ-4)
- `TestYieldingSource_MetaFunc` + nil-omits counter in `yield_test.go` (SetMetaFunc helper)
- `TestList_NextCursorOmitsWhenEmpty`, `TestList_NextCursorPresentWhenSet` (δ-5)

**Risk:** Low. Wire-breaking but the change is mechanical and the spec is firm.

**Deprecation policy:** Not applicable — wire-format change in an `experimental/` package.

---

### ε — Push delivery (`events/stream` + notifications surface)

**Status:** in flight on `feat/events-epsilon-stream`; 8 commits landed (ε-1 through ε-6 + the ε-4a CallContext refactor), PR pending. Three of the five spec notifications (active, event, heartbeat) plus StreamEventsResult are wired end-to-end. `notifications/events/error` and `/terminated` are SDK-side ready but not emitted server-side — they need source-side signals (transient/terminal failure state) that don't exist until ζ adds the deliveryStatus state machine.

**Goal:** real per-subscription push delivery, replacing today's broadcast-to-all-SSE-listeners model.

**Spec deltas addressed:**
- `events/stream` is a long-lived JSON-RPC request (one per subscription). Streamable HTTP: POST returns SSE response. stdio: notifications on stdout demuxed via `requestId`. (§"Push-Based Delivery" L199-206 + "Request: events/stream" L225-241)
- Server emits five new notifications, all carrying `requestId` (the JSON-RPC `id` of the parent stream): (§"Push-Based Delivery" → "Event Delivery" L243-271)
  - `notifications/events/active {requestId, cursor, truncated}` — confirmation, sent on subscribe and again on mid-stream gap recovery (L249)
  - `notifications/events/event {requestId, eventId, name, timestamp, data, cursor}` — payload (L252)
  - `notifications/events/heartbeat {requestId, cursor}` — keepalive ≥30s; carries cursor so client's persisted cursor advances during quiet periods. SSE `data:` frame, NOT comment form. (§"Push-Based Delivery" → "Lifecycle" → "Heartbeat" L270)
  - `notifications/events/error {requestId, error}` — transient per-event upstream failure; stream stays open (L255 + L261)
  - `notifications/events/terminated {requestId, error}` — subscription has ended (auth revoked, etc.); SDK removes the subscription (§"Authorization" L783-795)
- `StreamEventsResult {_meta: {}}` — empty typed final frame, sent only when server initiates the close (§"Push-Based Delivery" → "Lifecycle" → "Stream termination" L269)
- Client cancellation: HTTP abort or `notifications/cancelled` (stdio) (§"Push-Based Delivery" → "Lifecycle" → "Cancellation" L271)
- `events/stream` requests are exempt from any general request-concurrency cap (§"Push-Based Delivery" → "Lifecycle" → "Concurrent streams" L272)

**Files touched (as built):**
- `experimental/ext/events/yield.go` — `YieldingSource.Subscribe(ctx) <-chan SubscriberEvent` channel API + `WithSubscriberBuffer(n)` option (default 64). Drop-on-full policy: a slow subscriber doesn't back-pressure yield; dropped events surface as `Truncated=true` on the next successful send. (ε-1)
- New file `experimental/ext/events/stream.go` — `registerStream` + per-stream goroutine. `select(event, heartbeat, ctx.Done)` loop emits `notifications/events/{active, event, heartbeat}` and returns `StreamEventsResult{_meta:{}}` on cancel. Maps `SubscriberEvent.Truncated` onto a fresh `active{truncated:true}` per spec L285. (ε-2/ε-3)
- `experimental/ext/events/events.go` — `Config.StreamHeartbeatInterval` (default 30s); `Register()` wires `registerStream`. New `ErrCodeDeliveryModeUnsupported = -32017` for TypedSource (which lacks Subscribe). (ε-2)
- mcpkit core `client/client.go` — typed `CallContext` (per constraint C1): embeds `context.Context`, carries a per-call notification hook for long-lived calls. Streamable HTTP transport threads the hook into its SSE-frame loop; other transports stub. **Replaces** the briefly-introduced `CallWithOptions` functional-options API from ε-4a. (ε-4a)
- Go SDK `experimental/ext/events/clients/go/stream.go` (NEW) — `Stream(parent, sess, opts) (*StreamCall, error)` with typed callbacks `OnEvent`, `OnHeartbeat`, `OnTruncated`, `OnError`, `OnTerminated`. Returns once the initial `active` arrives; `Stop()` cancels the underlying call cleanly. (ε-4b)
- Python SDK `events_client.py` — `MCPSession.open_call_stream(method, params, callback)` + `cmd_stream` subcommand. Honors `--max-age`. Pretty-prints active / event / heartbeat / error / terminated frames. (ε-5)
- Discord + telegram demos — `notifBroker` removed entirely; push steps use `eventsclient.Stream`. Discord live step opens TWO concurrent streams (message + typing) demonstrating per-stream isolation. (ε-6)
- Discord + telegram `e2e_test.go` — new `TestE2EStreamDelivery` + `TestE2EStreamCursorless` alongside the preserved `TestE2EPushDelivery` (broadcast model kept until η deletes `events.Emit`).

**Borrowable from TS reference SDK (`modelcontextprotocol/typescript-sdk` branch `events-bufferemits-and-examples`):**
- Schema definitions for the five notifications: `EventActiveNotificationSchema`, `EventNotificationSchema`, `EventErrorNotificationSchema`, `EventTerminatedNotificationSchema`, `EventHeartbeatNotificationSchema` in `packages/core/src/types/schemas.ts`
- Their notification routing pattern in `packages/server/src/server/events.ts` (1614 lines — large but tested)
- Their integration test suite in `test/integration/test/server/events.test.ts` (2526 lines) covers most edge cases — port the conformance subset

The TS reference's heartbeat doesn't carry a cursor today; the spec now requires it. We get to ship this correctly the first time.

**Tests (as built):**
- `experimental/ext/events/yield_test.go` — `TestYieldingSource_Subscribe*` (4 tests: receive, multi-subscriber fanout, ctx-cancel cleanup, drop-on-slow-consumer with Truncated semantics) (ε-1)
- `experimental/ext/events/stream_test.go` — 7 tests via InProcessTransport with notification capture: validation rejection, active-first ordering, event with requestId, StreamEventsResult final frame, heartbeat timing + cursorless heartbeat (ε-2)
- `experimental/ext/events/stream_routing_test.go` — concurrent-stream isolation (different sources + same source) + gap-recovery active emission (ε-3)
- `client/client_per_call_notify_test.go` — `TestCallContext_*` (per-call hook fires AND global callback STILL fires — additive contract) (ε-4a)
- `experimental/ext/events/clients/go/stream_test.go` — `TestStream_DeliversEventsViaCallback`, `TestStream_HeartbeatCallback`, `TestStream_StopEndsTheCall`, `TestStream_RejectsInitialError` (ε-4b)
- `examples/events/{discord,telegram}/e2e_test.go` — `TestE2EStreamDelivery`, `TestE2EStreamCursorless` per demo (ε-6)

**Race-detector:** clean across all event modules. One pre-existing race in `examples/events/telegram/handlers_test.go:TestWebhookHMACSignature_MCPHeaders` is tracked as a separate issue (filed during the ε branch — captured-headers race in the httptest handler, predates this work).

**Reference comparison (TS SDK):** notification field names verified against `modelcontextprotocol/typescript-sdk` `EventActiveNotificationSchema` etc. Heartbeat carrying cursor (which the TS SDK doesn't yet do) shipped correctly per the updated spec.

**Risk:** Medium. New transport surface. Mitigations: keep the old `Server.Broadcast` path until all callers migrate; write the design doc commit first.

**Deprecation policy:** When ε ships, mark the `Emit(srv, event)` broadcast helper `// Deprecated:` with a pointer to per-stream delivery. Don't delete in this PR; that's a follow-up after the demo migration.

---

### ζ — Webhook hardening (delivery-time SSRF, body cap, control envelopes, deliveryStatus)

**Status:** in flight on `feat/events-zeta-webhook-hardening`. All 6 commits landed. PR 375 ready for review. ζ-7 (source-side health signals → notifications/events/error and /terminated server emissions) tracked separately as issue 376. Race-clean across all four event modules.

**Goal:** close the spec-mandated security and reliability gaps in our webhook delivery path.

**Spec deltas addressed:**
- **SSRF revalidation at delivery time**: hostname-based subscribe-time check is insufficient under DNS rebinding. Resolve hostname, check resolved IP against the blocklist, connect directly to that IP (sending the original hostname in `Host` header / TLS SNI). (§"Webhook Security" → "SSRF prevention" L464)
- **No HTTP redirects on webhook deliveries** — explicitly disable via `http.Client.CheckRedirect: ErrUseLastResponse`. Redirects can target an internal address that bypasses the blocklist. (§"Webhook Security" → "SSRF prevention" L464 — same paragraph)
- **Body size cap**: SHOULD ≤ 256 KiB; servers MUST treat 413 from the receiver as non-retryable. (§"Webhook Security" → "Delivery profile (for WAF / private-cloud deployments)" L487)
- **Control envelopes**: server POSTs signed `{type:gap, cursor:<fresh>}` when a gap is detected between refreshes; signed `{type:terminated, error:{...}}` when the subscription ends. Both use Standard Webhooks headers + `X-MCP-Subscription-Id`. `webhook-id` is `msg_<type>_<random>` (vs `eventId` for event deliveries — set up in α). (§"Non-event webhook bodies" L415-423)
- **`X-MCP-Subscription-Id` header** on every webhook delivery POST (the routing handle from γ; surfaced in the body-less header so receivers pick the right secret without parsing). (§"Webhook Event Delivery" L390 + §"Webhook Security" → "Signature scheme" L472)
- **`deliveryStatus`** on `events/subscribe` refresh response: `{active, lastDeliveryAt, lastError, failedSince?}`. `lastError` is a categorical string from a fixed set: `connection_refused | timeout | tls_error | http_4xx | http_5xx | challenge_failed`. **Never** raw response bodies / headers / status lines (avoids becoming a response oracle for attacker-chosen URLs). (§"Webhook Delivery Status" L425-460)
- **Suspend / reactivate state machine**: after repeated failures the server SHOULD set `active: false`; a successful refresh reactivates it (`active: true`) and resumes retrying pending events. (§"Webhook Event Delivery" L413 + §"Webhook Delivery Status" L460)

**Files touched (as built so far):**
- `experimental/ext/events/webhook.go` — `net.Dialer.Control` SSRF guard inspecting resolved IP at dial time (TOCTOU-safe). `CheckRedirect: ErrUseLastResponse` on outbound http.Client. Body cap (default 256 KiB) enforced REJECT-not-truncate at Deliver entry. 413 + 3xx classified as non-retryable. New `WithWebhookAllowPrivateNetworks(bool)` (default false; demos opt in) and `WithWebhookMaxBodyBytes(int)` (default 256 KiB) options. `ValidateWebhookURL` converted to a `*WebhookRegistry` method so it can read the per-instance allowPrivate setting; strict-by-default for obvious loopback hostnames at subscribe time. (ζ-1, ζ-2, ζ-3)
- New `experimental/ext/events/control.go` — `controlEnvelope`, `ControlError`, `WebhookRegistry.PostGap(canonicalKey, freshCursor)` and `PostTerminated(canonicalKey, ControlError)`. Single-attempt delivery (best-effort signal). PostTerminated removes the target from the registry regardless of POST outcome — no zombie entries. (ζ-4)
- `experimental/ext/events/headers.go` — `newControlMessageID(typ)` mints `msg_<typ>_<random>`, finally honoring α's reserved-form stub. (ζ-4)
- `experimental/ext/events/events.go` — registerSubscribe calls `webhooks.ValidateWebhookURL(...)` (method form). (ζ-1)
- Go SDK `experimental/ext/events/clients/go/receiver.go` — `OnGap(cb)` + `OnTerminated(cb)` chainable callback installers + `ControlError` type. ServeHTTP probes top-level `type` BEFORE Event unmarshal so the common case (no `type` → event) doesn't pay a double-decode tax. (ζ-4)
- Python SDK `events_client.py` — `_make_webhook_handler` dispatches by top-level `type`; pretty-prints `── WEBHOOK GAP / TERMINATED ──` frames distinctly. (ζ-4)
- Test fixtures across all four event modules opt into `WithWebhookAllowPrivateNetworks(true)` since httptest binds to 127.0.0.1; demo binaries do too with a comment that production deployments leave it OFF.

**Files touched (ζ-5, ζ-6 — landed):**
- `experimental/ext/events/webhook.go` — `DeliveryStatus{Active, LastDeliveryAt, LastError, FailedSince}` + `DeliveryErrorBucket` typed string (categorical: connection_refused, timeout, tls_error, http_3xx_redirect, http_4xx, http_5xx, challenge_failed). `recordDeliverySuccess` / `recordDeliveryFailure` helpers + `classifyTransportError` (type-asserted markers, no err.Error() leakage). Suspend state machine: counter + first-failure-time + last-success-time per target; sliding window. `Targets()` filter excludes suspended; `Register` refresh path reactivates suspended targets. New `WithWebhookSuspendThreshold(n)` (default 5) + `WithWebhookSuspendWindow(d)` (default 10min) options.
- `experimental/ext/events/events.go` — `registerSubscribe` refresh response includes `deliveryStatus` via new `deliveryStatusForResponse` projector when target has prior delivery attempts. Omitted on first subscribe (nothing to report).

**Acceptance (as verified so far):**
- All four event modules pass `go test -count=1 -race ./...` after each commit (ζ-1 through ζ-4)
- SSRF dial-time guard rejects 10 IP families (loopback, RFC1918, link-local, ULA, IPv4-mapped-IPv6, etc.) — pinned by table-driven `TestDelivery_DialBlocklistRanges`
- 3xx redirect not followed — pinned by `TestDelivery_DoesNotFollowRedirects`
- Oversized event not POSTed (REJECT not TRUNCATE) — pinned by `TestDelivery_OversizedEventNotPosted`
- 413 from receiver = exactly 1 attempt — pinned by `TestDelivery_413NotRetried`
- Control envelope wire shape (top-level `type`, `msg_<typ>_<random>` webhook-id, X-MCP-Subscription-Id presence) — pinned by `TestControlEnvelope_*` (server side) + `TestReceiver_RoutesGap/TerminatedToCallback` (SDK side)

**Acceptance for ζ-5, ζ-6 (landed):**
- `deliveryStatus` round-trip: refresh after success populates `lastDeliveryAt`; refresh after 5xx shows `lastError: http_5xx`; oracle-body test confirms raw response data NEVER leaks
- Suspend: N consecutive failures within W → Active=false; failures outside W don't accumulate (sliding-window reset); successful refresh reactivates and clears state; suspended target skipped in Deliver fan-out

**Tests added so far:**
- `ssrf_delivery_test.go` — 5 tests (loopback rejected, escape allows, blocklist table, public allows, redirects rejected)
- `body_cap_test.go` — 4 tests (oversized rejected, oversized logged, 413 not retried, default cap pin)
- `control_envelope_test.go` (server) — 3 tests (gap shape, terminated shape, top-level type discriminator)
- `clients/go/control_envelope_test.go` (SDK) — 3 tests (routes gap, routes terminated, no-callback-installed safe)

**Tests added (ζ-5, ζ-6 — landed):**
- `delivery_status_test.go` — 8 tests: deliveryStatus omission on first subscribe, lastDeliveryAt after success, lastError categorical (oracle-body leak check), connection_refused bucket, suspend after threshold, sliding-window reset, refresh reactivation, suspended skipped in Deliver

**Risk:** Low to medium. Most changes are additive (new envelope types, new header, new field on response). The SSRF + redirect changes touch the http.Client setup, easy to land safely.

**Deprecation policy:** Not applicable — additive.

---

### η — SDK hooks (deferred until ε settles)

**Goal:** add the per-subscription `match`/`transform` async hooks, `on_subscribe`/`on_unsubscribe` lifecycle hooks, and poll-lease tracking.

**Why deferred:** the design needs more thought than the others. The spec's hook surface — `match`/`transform` per-subscription (§"Server SDK Guidance" L599-635) and `on_subscribe`/`on_unsubscribe` lifecycle (§"Server SDK Guidance" → "Subscription lifecycle hooks" L667-691) — is described in Python-flavored prose; mapping it to Go's type system + concurrency model is its own design exercise. Worth doing *after* ε proves out the per-subscription event flow, since these hooks gate per-subscription delivery decisions.

**When to revisit:** after ε merges and the discord/telegram demos run on real `events/stream`. By that point we'll have load-bearing per-subscription state to hang the hooks off, instead of bolting them onto the broadcast path.

**Out of scope for this plan:** the design questions for η will be captured in their own plan when we're ready to start.

## Dependency graph

```
α (renames)  ──┐
               ├─→ γ (auth + tuple identity) ──→ ζ (control envelopes use the id from γ)
β (secret    ──┤                                  delivery-time SSRF)
   collapse)   │
               └─→ δ (flat poll, maxAge)
                    │
                    └─→ ε (push) ──→ η (SDK hooks)
```

Reading: α and β can land in either order. γ depends on β (no point keying on (principal, url, name, params) until the secret model is the spec's). δ depends only on α (rename hygiene). ε can technically start any time but benefits from γ being in place (per-stream auth gating) and ζ landing first (the delivery state machine ε's stream lifecycle borrows from). η is post-ε.

In practice, recommend serial: α → β → γ → δ → ε → ζ → η. Each PR ~1-3 days for α-δ; ε is the long pole (1-2 weeks); ζ is back to 1-3 days.

## Deprecation policy

For removals that are spec-mandated *but the spec is still moving*, use the deprecate-then-delete pattern from phase 1:

```go
// Deprecated: Per MCP Events spec sketch revision 2026-04-30, X has been
// removed in favor of Y. Kept here behind the EnableX config flag while
// the spec stabilizes.
// TODO(events-spec-alignment): delete once spec freezes.
```

Pattern:
1. Go's `// Deprecated:` godoc convention (golangci-lint surfaces it)
2. Date + spec reference (no upstream PR backlinks per repo hygiene rules — cite by spec section name + revision date)
3. Explicit removal trigger and grep target: `TODO(events-spec-alignment)`
4. Optional config flag if runtime opt-in is useful for testing

**When to deprecate vs delete:**
- **Deprecate** when the *concept* might come back in a different shape (e.g., if push notification routing semantics shift again)
- **Delete** when the spec has settled and no plausible reversal exists (β's secret modes — the spec actively simplified, no path back to three modes)

For this plan: α deletes the old field names cleanly (no deprecation — just rename). β deletes the secret modes cleanly. γ deletes the old subscription-identity shape cleanly. δ deletes the wrapper. ε retains the broadcast `Emit` path with a `// Deprecated:` marker for one release cycle, then deletes in η.

## Out of scope

- **Cross-server event routing** — application-layer concern (sketch line 823)
- **Event-bound prompts** — deferred to a future spec version (sketch line 824)
- **Guaranteed exactly-once delivery** — application-layer dedup via `eventId` (sketch line 825)
- **Resource subscriptions / list_changed reframed as events** — Open Question 2 in the spec; we don't preempt this
- **MCP task state changes as events** — Open Question 5
- **Endpoint verification challenge** — Open Question 6; ζ adds the other two control envelopes but leaves verification as a TODO
- **High-frequency / streaming media** — explicitly out of scope upstream

## Tracking

- Public umbrella: #349 (mirrors the shape of #323)
- Per-PR detail: each PR's description + a focused root `PLAN.md` for ε if needed
- Phase 1 reference: #323 (closed, all PRs merged)
- This plan: `docs/EVENTS_SPEC_ALIGNMENT_PLAN.md` (this file) — updated as PRs land

## How to use this plan

- Start a PR group: read its section, open a feature branch, file an issue if it'll need cross-WG visibility
- Mid-PR: if scope changes, update the relevant section here in the same PR
- After PR merges: tick the group as "done" by updating its Status line and the TL;DR table
- New spec revision lands: add a new section "Spec revision YYYY-MM-DD changes" with the new deltas, don't rewrite history