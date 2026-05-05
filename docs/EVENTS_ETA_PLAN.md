# Events η Plan — SDK Hooks (match / transform / on_subscribe / on_unsubscribe / poll-lease)

**Status:** Drafted 2026-05-05. The last unshipped group from `docs/EVENTS_SPEC_ALIGNMENT_PLAN.md` (the parent plan's η section, lines 355-363, said "Out of scope for this plan: the design questions for η will be captured in their own plan when we're ready to start." This is that plan.).
**Scope:** `experimental/ext/events/` Go library. SDK side only — no new wire surface, no new spec methods. The hooks live entirely in author-facing Go API.
**Spec target:** `mcpcontribs/proposals/triggers-events-wg/spec-snapshots/design-sketch-2026-04-30.md` §"Server SDK Guidance" L580-715. Same citation convention as the parent plan: `(§"Section name" L<line>)` references the cached snapshot.

## TL;DR

Six sub-PRs, dependency-ordered. Total ~600-900 LOC including tests. The infrastructure (η-1 poll-lease, η-2 hook registry) is bigger than the per-mode wiring (η-3/4/5/6). Reasonable to land η-1 + η-2 together as a foundation PR, then the four wiring PRs in parallel.

| Sub-PR | Theme | Rough size | Risk | Why this position |
|---|---|---|---|---|
| **η-1** | Poll-lease table — infrastructure for `on_subscribe`/`on_unsubscribe` parity across modes | ~200 LOC + tests | Low (in-memory soft state) | Foundation — η-3 can't fire on_subscribe for poll without it. |
| **η-2** | Hook registration on `EventDef` — author-facing API for match/transform/on_subscribe/on_unsubscribe | ~150 LOC | Low (option-pattern surface) | Defines the contract every later sub-PR plugs into. |
| **η-3** | on_subscribe / on_unsubscribe wiring across all three delivery modes (push close, webhook unregister/TTL, poll lease) | ~150 LOC | Medium (lifecycle ordering) | Needs η-1 + η-2 in place. |
| **η-4** | match / transform hooks on broadcast emit — push fanout (yield.go Subscribe loop), webhook fanout (Deliver loop), poll-from-emit-buffer | ~200 LOC | Medium (per-event hot path) | Needs η-2; can land in parallel with η-3. |
| **η-5** | Targeted emit by subscription id — skip match-fanout when caller already knows the target | ~80 LOC | Low | Needs η-4's emit refactor. |
| **η-6** | `TooManySubscriptions` enforcement — per-event-type cap, per-principal cap; rejection before on_subscribe fires | ~100 LOC | Low | Independent; lands last so the per-mode handlers all see the same rejection contract. |

The pattern across the six: **one author API, four call sites** (events/poll, events/stream, events/subscribe, the emit fanout). Most of the implementation cost is wiring the same hook through four places, not the hook surface itself.

## Why now

The parent plan's gating condition was met:

> "Worth doing *after* ε proves out the per-subscription event flow, since these hooks gate per-subscription delivery decisions." (`EVENTS_SPEC_ALIGNMENT_PLAN.md` line 361)

ε shipped (events/stream + 5 notification frames + StreamEventsResult, plus the per-stream Subscribe channel in yield.go), and ζ shipped on top of it (suspend/control envelopes/deliveryStatus). The per-subscription state the hooks need to hang off of is now load-bearing — `WebhookRegistry.Register/Unregister` for webhook, `YieldingSource.Subscribe(ctx)` per stream for push. Poll is the only mode without per-subscription state, which η-1 fixes.

**A second motivation:** the planned multi-source kitchen-sink demo in `mcpcontribs/proposals/triggers-events-wg/poc-notes.md` ("kitchen-sink multi-source / multi-consumer example", post-η backlog) needs match/transform to demonstrate filtering by subscription params. Without it, the demo can't show "subscriber A watches channel X, subscriber B watches channel Y, same event source" — the differentiator the spec was designed for.

## Open design questions (Go ↔ Python mapping)

The spec is Python-flavored. These are the choices Go forces on us; flagging here so the per-sub-PR sections can reference resolved decisions instead of relitigating.

### Q1 — Where do match/transform live? Field on EventDef? Method on the Source? Functional option?

**Three candidates:**

(a) **Field on `EventDef`.** Add `Match func(...) bool` and `Transform func(...) Event` to the struct. Authors set them when constructing the EventDef.

(b) **Method on the source via interface.** Define `interface { Match(ctx, event, params) bool; Transform(ctx, event, params) Event }`. Sources opt in by implementing it.

(c) **Functional options on `events.Register`.** `events.Register(events.Config{...}, events.WithMatchFor("alert.fired", matchFn))`.

**Recommend (a)** — field on EventDef. Reasons:
- The hooks are *per event type*, same scope as `Cursorless`, `PayloadSchema`, `Delivery`. Putting them on EventDef keeps the author's mental model — "everything I want to say about this event type lives in one place."
- The interface route (b) forces every source author to implement two extra methods even when they don't need filtering. Library has both `YieldingSource[Data]` and `TypedSource[Data]`; doubling the interface surface doubles the boilerplate.
- Functional options (c) are good when behavior depends on cross-cutting library state (TTL, header mode). These hooks depend on per-event-type state, so they belong with the event type.
- Go-specific: `Match`/`Transform` are zero-value-friendly (`nil` = "not set" = "all subscribers receive, payload unchanged"), which matches the spec's "If absent, all subscriptions for that event name receive it."

**Tradeoff:** Putting funcs on a struct that's also used over the wire (`EventDef` is JSON-serialized for `events/list`) means the hook fields need `json:"-"` tags. Cosmetic; well-precedented in Go (see how `http.Server` mixes serializable config with handler functions).

### Q2 — What's the context type for match / transform?

The spec says "`ctx` carries the subscription's principal and request metadata." Go has `context.Context` and mcpkit has `core.MethodContext`.

**Recommend a new typed `events.HookContext`** wrapping the standard library `context.Context` plus:
- `Principal() string` — the subscription's resolved principal (post-`UnsafeAnonymousPrincipal` fallback)
- `SubscriptionID() string` — the server-derived `sub_<base64>` (only set for webhook + push; nil/zero for poll where the lease key is the principal-eventname-paramshash tuple, not a subscription id)
- `Mode() DeliveryMode` — `DeliveryModePoll` / `DeliveryModePush` / `DeliveryModeWebhook` so authors can implement mode-aware logic if they need to
- `Context() context.Context` — exposed for cancellation / deadlines

Reusing `core.MethodContext` directly would be wrong: that type is shaped around handler invocation (carries id, request method, send-side hooks like `Notify`); it doesn't make sense per-emit-fanout-iteration.

### Q3 — sync vs async hook signature?

The spec uses Python `async`. Go has no async; the natural mapping is sync functions that may take a `context.Context`.

**Recommend sync signatures**:
```go
type MatchFunc func(ctx HookContext, event Event, params map[string]any) bool
type TransformFunc func(ctx HookContext, event Event, params map[string]any) Event
type SubscribeFunc func(ctx HookContext, params map[string]any) error
type UnsubscribeFunc func(ctx HookContext, params map[string]any)
```

`Match`/`Transform` fire on the per-event hot path (one yield → N subscribers → 2N hook calls); they MUST be cheap. Authors that need I/O in `Match` (rare — typically just inspect event.data + params) can defer to a precomputed cache or use the `HookContext.Context()` for cancellation.

`on_subscribe` returns `error` so the author can refuse provisioning (e.g., upstream API quota exceeded). Mapped to `-32013 TooManySubscriptions` or a fresh `-32014 SubscribeFailed` (need to check spec error code allocation).

`on_unsubscribe` doesn't return error — it's notification-style; failures get logged and swallowed.

### Q4 — When does `on_subscribe` fire for each mode?

| Mode | on_subscribe fires when | on_unsubscribe fires when |
|------|-------------------------|---------------------------|
| **Webhook** | First Register call for a (principal, name, params, url) tuple (NOT on refresh — refresh is renewal of the same subscription) | `Unregister` (explicit), `pruneExpiredLocked` (TTL), `PostTerminated` (server-initiated death), `postTerminatedSilent` (suspend transition — debatable; see below) |
| **Push** | Stream open, after auth + source.Subscribe channel acquired (in `registerStream`'s body, before the select loop) | Stream close — both ctx.Done() and chan-closed paths in `registerStream` |
| **Poll** | First poll for a (principal-or-anon, name, canonicalHash(params)) lease key — see η-1 | Lease expires without renewal — see η-1 |

**Open sub-question:** does on_unsubscribe fire on the suspend transition (`postTerminatedSilent`)? Argument for: receiver can't get more events, it's effectively unsubscribed. Argument against: refresh reactivates, and re-firing on_subscribe on every reactivation churns upstream resources for unstable receivers. **Recommend: don't fire on suspend.** The subscription is still in the registry as `Active=false`; it's "paused" not "removed". on_unsubscribe fires only on actual registry deletion (Unregister, TTL, PostTerminated). Document this explicitly in the hook's godoc.

### Q5 — Where does the poll-lease table live?

A new package-level type or a field on `Config`?

**Recommend: a new exported type `PollLeaseTable` with sensible defaults**, instantiated inside `events.Register` if the author hasn't provided one via `Config.PollLeases`. Symmetric with `WebhookRegistry` — both are SDK-soft-state stores with TTL, both surface lifecycle hooks.

Keying: `(principal-or-anon, eventName, canonicalHash(params))` per spec L707. canonicalHash is the same canonical-JSON-of-params encoding `identity.go` already uses for webhook subscriptions; we can lift `canonicalKey` directly (or extract a shared helper). Anonymous principal → empty string (or a sentinel) for the principal slot, per spec "on unauthenticated servers all callers share one lease per (name, params)."

Default TTL: `5 * defaultStreamHeartbeatInterval = 150 seconds` was the parent plan's hint; spec L711 example uses `timedelta(minutes=5)`. **Recommend: 5 minutes (`300s`)** — round number, well above any reasonable `nextPollSeconds` (we currently emit `5` from `pollResultWire.NextPollSeconds`; 60x headroom). Configurable via `WithPollLeaseTTL(d)`.

### Q6 — Targeted emit signature?

The spec shows two `server.emit()` patterns:
```python
server.emit(Event(...))                                    # broadcast — match/transform per sub
server.emit(Event(...), subscription_id="sub_abc")         # targeted — skip match
```

**Recommend two functions, not one with optional arg:**
```go
events.Emit(srv, event)                       // broadcast — existing function, η-4 adds match/transform pass
events.EmitToSubscription(srv, event, subID)  // targeted — η-5 adds; skips match/transform; routes only to that sub
```

Reason: variadic-options style for what is conceptually two different operations is harder to read at the call site, and `subscription_id` is a load-bearing field — accidentally calling broadcast emit thinking it was targeted (or vice versa) would silently fan out wrongly. Two function names = two wire-routing semantics, immediately greppable.

Targeted emit needs to know which active subscriptions exist (to look up the right push stream / webhook target). Today both `YieldingSource.subscribers` (per-source, per-stream) and `WebhookRegistry.targets` (per-source-not-encoded, registry-keyed-by-canonical) hold that info but don't share an indexing scheme. **η-5 adds a thin index: `subID → (mode, stream-or-target)`** maintained by η-3's lifecycle wiring.

### Q7 — `TooManySubscriptions` enforcement scope?

Spec L705 says "Servers SHOULD enforce TooManySubscriptions before invoking on_subscribe."

Two reasonable enforcement axes:
1. **Per event type, total** — "no more than 1000 subscriptions to `discord.message`"
2. **Per principal, per event type** — "no more than 10 subscriptions per user to `discord.message`"
3. **Per principal, total** — "no more than 100 subscriptions per user across all event types"

**Recommend (2)** as the default — it's what protects against runaway clients without arbitrarily limiting the server's overall capacity. (1) and (3) can be added behind options later if a deployment needs them. Wire as `WithMaxSubscriptionsPerPrincipal(eventName, n)` returning a `WebhookOption` (and a parallel for poll-lease).

### Q8 — Hook execution under load?

Hot-path concerns when match/transform fire:
- **Time per call:** the per-emit fanout already takes a write lock in `yield.go fanoutLocked`. Hooks running under that lock would serialize the whole source. **Recommend:** snapshot the live subscriber list under the lock, drop the lock, then iterate-and-call hooks in the goroutine that called yield. (Current Subscribe-channel send is non-blocking with drop-with-Truncated semantics; same model works.)
- **Per-subscription panic:** an author's bad match function shouldn't take down the whole fanout. Wrap each call in a defer/recover, log the panic, treat as "match returned false" (skip this subscriber). Same for transform — log + skip.
- **Allocation:** transform returning a new Event per subscriber means N allocations per emit. For sources with many cheap subscribers, this is wasteful. **Recommend:** make `Transform` return `(Event, bool)` where the bool says "I modified the event; use the returned value" vs "I didn't; reuse the original." Authors who pass through unchanged events return `(orig, false)` and we skip the per-sub allocation. (Optimization; not blocker.)

## Approach

### Principles (inherited from parent plan)

1. **One delta per PR** — η-1 is poll-lease infrastructure, η-2 is the hook surface. Don't bundle.
2. **Wire-level commits** — every commit message names the spec section / line / rationale.
3. **Don't delegate understanding** — every PR description names file paths + before/after behavior.
4. **Independent PRs** — η-3, η-4, η-5, η-6 should each be mergeable on their own.
5. **No backwards-compatibility shims** — η is purely additive (adds new opt-in hooks); no deprecations needed.

### What's out of scope

- **Hook persistence across restart.** The lease table is ephemeral by design (spec L715). on_subscribe re-fires on first poll after restart, which is the correct behavior — don't try to durable-store the lease table.
- **Match/transform for the existing TypedSource path.** TypedSource owns its own storage and Poll() callback; the author can apply match/transform inside their callback already. Adding hook plumbing to TypedSource would duplicate logic. Document this in η-4: "match/transform fire for events emitted via `Emit` / `EmitToWebhooks` (the YieldingSource path); TypedSource authors apply filtering in their `Poll` callback directly."
- **Per-subscription rate limiting / quotas.** Belongs in middleware (extension-mechanisms Q4 lists middleware as a dedicated extension point), not in the hook surface.
- **Cross-process / clustered SDK state.** Spec L946-963 acknowledges horizontally scaled servers need shared state (Redis suggested) but explicitly says protocol does not require it. Keep η as in-process; document this constraint in `DEPLOYMENT.md`.

## PR groups

### η-1 — Poll-lease infrastructure

**Status:** not started.

**Goal:** add an in-memory soft-state table that tracks which (principal, name, params) tuples have recent poll activity, with TTL-based expiry and a callback fired when a lease is created or expires. Foundation for η-3's poll-mode lifecycle hooks.

**Spec deltas addressed:**
- `(§"Server SDK Guidance" → "Unsubscribe timing by mode" L707-715)` — "the SDK treats poll subscriptions as leased. The lease is keyed on `(principal-or-null, eventName, canonicalHash(params))`... The lease window is SDK-configurable and SHOULD default to a small multiple of the server's typical `nextPollSeconds`."

**Files touched:**
- `experimental/ext/events/poll_lease.go` (new) — `PollLeaseTable` type, `Touch(principal, name, params)` (returns true if newly created), `Expire()` background goroutine, `OnCreate / OnExpire` callbacks. Reuses `canonicalKey` from `identity.go`.
- `experimental/ext/events/poll_lease_test.go` (new) — TTL behavior, anonymous-principal coalescing, OnCreate/OnExpire firing order, concurrent-Touch safety.
- `experimental/ext/events/events.go` — `Config.PollLeases *PollLeaseTable` field; `events.Register` instantiates a default if nil; `registerPoll` wraps each handled poll in `Touch` (no behavior change yet — η-3 hooks the OnCreate/OnExpire callbacks).

**Acceptance:**
- A test that calls `events/poll` repeatedly for the same (principal, name, params) sees one OnCreate fire and zero OnExpire fires (lease keeps renewing).
- A test that calls `events/poll` once then waits >TTL sees one OnCreate then one OnExpire.
- A test with two distinct param sets sees two OnCreate fires.
- A test with anonymous principal (no auth) sees all callers share one lease.
- `make test-experimental` green; no regressions in poll behavior.

**Tests:** new file `poll_lease_test.go` plus an integration test in `events_test.go` (or wherever the existing poll handler tests live) verifying Touch is called on every poll request.

**Risk:** Low. In-memory soft state with a background goroutine; precedent in `WebhookRegistry.pruneExpiredLocked`.

**Deprecation policy:** N/A — purely additive.

---

### η-2 — Hook registration surface on EventDef

**Status:** not started.

**Goal:** add `Match`, `Transform`, `OnSubscribe`, `OnUnsubscribe` fields to `EventDef`. These are zero-value-friendly (nil = no-op). Defines the contract the four wiring sub-PRs (η-3, η-4) plug into. No behavior change yet — fields are accepted and stored but not invoked.

**Spec deltas addressed:**
- `(§"Server SDK Guidance" L623-629)` — match/transform hooks
- `(§"Server SDK Guidance" → "Subscription lifecycle hooks" L691-705)` — on_subscribe/on_unsubscribe

**Files touched:**
- `experimental/ext/events/events.go` — add `EventDef.Match`, `EventDef.Transform`, `EventDef.OnSubscribe`, `EventDef.OnUnsubscribe` (all `json:"-"`-tagged so they don't leak onto the wire in `events/list`). Add `HookContext` interface + `hookContext` impl carrying principal, subscriptionID, mode, stdlib context.
- `experimental/ext/events/hooks.go` (new) — `MatchFunc`, `TransformFunc`, `SubscribeFunc`, `UnsubscribeFunc` type aliases; `DeliveryMode` enum (`DeliveryModePoll`, `DeliveryModePush`, `DeliveryModeWebhook`); panic-recovery wrappers `safeMatch`, `safeTransform`, `safeOnSubscribe`, `safeOnUnsubscribe` that the wiring sub-PRs use.
- `experimental/ext/events/hooks_test.go` (new) — tests for the panic-recovery wrappers (a panicking match returns false; a panicking transform returns the original event; lifecycle panic logs + swallows).

**Acceptance:**
- An EventDef with all four hooks set serializes through `events/list` without exposing the function fields (verify with a JSON round-trip test).
- The safe wrappers handle nil hooks (no-op) and panicking hooks (recover + log + return safe default) correctly.

**Tests:** unit tests in `hooks_test.go`; one integration test that registers a source with hooks set and verifies `events/list` response shape is unchanged.

**Risk:** Low. Surface-area definition; all wiring is in later PRs.

**Deprecation policy:** N/A.

---

### η-3 — Lifecycle hook wiring across all three delivery modes

**Status:** not started. Depends on η-1 + η-2.

**Goal:** wire `OnSubscribe` / `OnUnsubscribe` to fire at the right moment for each delivery mode. Authors implement the hook once on the EventDef; the SDK fires it from poll, push, and webhook code paths consistently.

**Spec deltas addressed:**
- `(§"Server SDK Guidance" → "Subscription lifecycle hooks" L691-705)` — "These hooks are called by the SDK across all delivery modes."
- `(§"Server SDK Guidance" → "Unsubscribe timing by mode" L707-715)` — push fires on stream close, webhook on Unregister/TTL, poll on lease expiry.

**Files touched:**
- `experimental/ext/events/events.go` — `registerSubscribe` calls `safeOnSubscribe` after successful `webhooks.Register` (only on first registration, not refresh — check `existing.ok` from Register's return). `registerUnsubscribe` calls `safeOnUnsubscribe` after `webhooks.Unregister` (only if a target was actually removed).
- `experimental/ext/events/webhook.go` — `pruneExpiredLocked` invokes `safeOnUnsubscribe` for each pruned target (need to thread the EventDef map down or store it on `WebhookTarget`). `PostTerminated` invokes `safeOnUnsubscribe` after registry deletion. `postTerminatedSilent` does NOT fire on_unsubscribe (Q4 decision: suspend ≠ unsubscribe).
- `experimental/ext/events/stream.go` — `registerStream` invokes `safeOnSubscribe` after `sub.Subscribe(ctx)` succeeds; defers `safeOnUnsubscribe` to run on either ctx.Done or chan-closed return paths.
- `experimental/ext/events/events.go` — `registerPoll` wires η-1's `PollLeaseTable.OnCreate` to `safeOnSubscribe` and `.OnExpire` to `safeOnUnsubscribe` at `Register` time.
- `experimental/ext/events/lifecycle_test.go` (new) — end-to-end tests: subscribe-then-unsubscribe webhook fires both hooks once; stream-open-then-close fires both hooks once; poll-then-wait-TTL fires both hooks once; refresh-after-subscribe does NOT re-fire on_subscribe; suspend transition does NOT fire on_unsubscribe.

**Acceptance:**
- Hooks fire exactly once per subscription lifecycle in each mode.
- Refresh of an existing webhook subscription does NOT re-fire on_subscribe.
- TTL expiry of a webhook subscription fires on_unsubscribe.
- Suspend transition (ζ-6 / ζ-7.3) does NOT fire on_unsubscribe; subsequent reactivating-refresh does NOT re-fire on_subscribe (the subscription was always there, just paused).
- Poll-lease expiry fires on_unsubscribe.
- `make test-experimental` green.

**Tests:** new `lifecycle_test.go` with one test per mode + the edge cases (refresh, suspend, TTL).

**Risk:** Medium. Lifecycle ordering is the kind of thing that gets subtle bugs. Plan to write each test BEFORE the corresponding wiring (red-then-green).

**Deprecation policy:** N/A.

---

### η-4 — match / transform on broadcast emit

**Status:** not started. Depends on η-2.

**Goal:** fire `Match` and `Transform` per-subscription when an event is emitted via `Emit` (push) or `EmitToWebhooks` (webhook). Authors filter and shape per-subscription without touching wire code.

**Spec deltas addressed:**
- `(§"Server SDK Guidance" L623-629)` — broadcast emit calls match then transform per active subscription.

**Files touched:**
- `experimental/ext/events/yield.go` — `fanoutLocked` (push subscribers) snapshots subscribers under lock, drops lock, then iterates-and-applies `safeMatch` + `safeTransform` per subscriber. Each subscriber needs its `(principal, params)` known — η-2 surfaces this on the subscriber slot. Subscribe channel currently doesn't track params; η-3's per-stream OnSubscribe call is the right place to plumb them through (extend `subscriberSlot` with `principal string`, `params map[string]any`).
- `experimental/ext/events/webhook.go` — `Deliver` iterates targets; per-target call `safeMatch` (skip if false) then `safeTransform` (use returned event for the body). Re-marshal the body per target if transform modified the event — only when transform actually returned `(_, true)`.
- `experimental/ext/events/events.go` — `registerPoll` reading from a YieldingSource's emit-only ring buffer applies `safeMatch` + `safeTransform` so poll subscribers see the same filtering and shaping as push/webhook subscribers (per spec L629).
- `experimental/ext/events/match_transform_test.go` (new) — an EventDef with a Match that returns false for some params filters those subscribers out; a Transform that redacts a field is reflected in the delivered event; nil hooks are no-op; panicking hooks are recovered to safe defaults.

**Acceptance:**
- One emit + two subscribers with different params → match called twice, transform called per subscriber that matched, each subscriber receives appropriately-shaped event.
- Webhook delivery body is re-signed (HMAC re-computed) per target when transform returned `(_, true)`. (Otherwise the same signed body is reused — the body cap (ζ-3) is checked per-target in case transform produces a different size.)
- Push subscribers, webhook targets, and poll callers all see consistent match/transform behavior for the same event.
- TypedSource is documented to apply filtering in its own Poll callback (no hook plumbing on the TypedSource path — see "What's out of scope").
- `make test-experimental` green.

**Tests:** new `match_transform_test.go` with cross-mode parity assertions.

**Risk:** Medium. Hot path; per-subscriber transform-and-re-marshal could be costly. Q8 above flags the optimizations to consider; first-pass implementation can be naive (always re-marshal) and we add the `(Event, bool)` short-circuit if profiling says it matters.

**Deprecation policy:** N/A.

---

### η-5 — Targeted emit by subscription id

**Status:** not started. Depends on η-4.

**Goal:** add `events.EmitToSubscription(srv, event, subID)` for authors who already know which subscription the event belongs to (typical when `on_subscribe` provisioned a per-subscription upstream listener that returns events tagged with the sub id).

**Spec deltas addressed:**
- `(§"Server SDK Guidance" L630)` — "Targeted emit. The server emits an event to a specific subscription by ID. This is appropriate when the server has set up a per-subscription upstream listener and already knows which subscription the event belongs to."

**Files touched:**
- `experimental/ext/events/emit_targeted.go` (new) — `EmitToSubscription(srv *server.Server, event Event, subID string)` function. Looks up the sub id in the subscription index (next bullet).
- `experimental/ext/events/subscription_index.go` (new) — thin in-memory map `subID -> {mode, ref}` where `ref` is either a `*subscriberSlot` (push) or a `WebhookTarget` (webhook). Maintained by η-3's lifecycle wiring (insert in OnSubscribe code paths, delete in OnUnsubscribe).
- `experimental/ext/events/yield.go` — refactor `fanoutLocked` to accept a `targetSubID string` filter (empty = broadcast); `EmitToSubscription` calls into it with the sub id.
- `experimental/ext/events/webhook.go` — analogous `Deliver(event)` accepts an optional subID filter.
- `experimental/ext/events/emit_targeted_test.go` (new) — emit to subID X with two active subs (X, Y) → only X receives; emit to nonexistent subID → drop with warn log; emit-to-targeted skips match/transform (the spec says targeted = author already decided this sub is the right recipient).

**Acceptance:**
- `EmitToSubscription` delivers to exactly the named subscription regardless of match.
- An unknown subID logs a debug warning and drops (don't panic, don't error — the subscription may have just expired between author's check and emit).
- Targeted emit skips both match and transform (spec L630 implies author has already shaped the event for this specific sub).
- Poll mode is not addressable by subID (poll subscriptions don't have an id; they have a lease tuple). `EmitToSubscription` for a sub id that doesn't exist in the push/webhook index treats this as "drop with warn." Document this clearly.

**Tests:** new `emit_targeted_test.go`.

**Risk:** Low. Well-bounded surface.

**Deprecation policy:** N/A.

---

### η-6 — TooManySubscriptions enforcement

**Status:** not started. Independent of η-3/4/5.

**Goal:** enforce a per-principal-per-event-type subscription cap. Reject new subscribes (and lease creations) with `-32013 TooManySubscriptions` BEFORE on_subscribe fires, so a rejected subscription never provisions upstream resources.

**Spec deltas addressed:**
- `(§"Server SDK Guidance" → "Subscription lifecycle hooks" L705)` — "Servers SHOULD enforce TooManySubscriptions before invoking on_subscribe."
- Existing error code `-32013 TooManySubscriptions` from α (PR #353) — currently defined in `errors.go` but not raised anywhere. η-6 makes it real.

**Files touched:**
- `experimental/ext/events/quota.go` (new) — `Quota` type tracking active subs by `(principal, eventName)`; `Reserve(principal, eventName) error` returns `ErrTooManySubscriptions` if cap exceeded; `Release(principal, eventName)` on unsubscribe. Configurable per-event-type via `WithMaxSubscriptionsPerPrincipal(eventName, n)` option on `Config`.
- `experimental/ext/events/events.go` — `registerSubscribe` calls `Reserve` BEFORE `webhooks.Register` (and BEFORE any potential on_subscribe fire); on Reserve failure, return `-32013 TooManySubscriptions`. `registerUnsubscribe` and webhook expiry call `Release`.
- `experimental/ext/events/stream.go` — `registerStream` calls `Reserve` before subscribing to the source; defers `Release` on stream close.
- `experimental/ext/events/poll_lease.go` — `Touch` calls `Reserve` on lease creation; OnExpire calls `Release`.
- `experimental/ext/events/quota_test.go` (new) — cap of 2 per-event-type per-principal: third subscribe returns `-32013`; unsubscribe one then resubscribe succeeds; no-cap (zero or absent) means unlimited; cross-event-type subscriptions don't share the budget; cross-principal subscriptions don't share the budget.

**Acceptance:**
- A principal with `WithMaxSubscriptionsPerPrincipal("alert.fired", 2)` configured can create exactly 2 webhook subscriptions to `alert.fired`; the 3rd returns `-32013`.
- Same cap applies to push and poll modes consistently.
- Unsubscribe / TTL / lease-expire releases the budget, allowing a fresh subscribe.
- on_subscribe fires only AFTER quota check passes.
- `-32013` payload includes a human-readable message naming the event-type and the cap.

**Tests:** new `quota_test.go`.

**Risk:** Low. Independent counter; no interaction with hot path.

**Deprecation policy:** N/A.

---

## Dependency graph

```
η-1 (poll-lease)  ──┐
                     ├──→ η-3 (lifecycle wiring)  ──┐
η-2 (hook surface)──┤                                ├──→ η-5 (targeted emit)
                     └──→ η-4 (match/transform)  ───┘

η-6 (quota) — independent; can land any time after α (which defined the error code)
```

Reading: η-1 + η-2 are the foundation; can land in either order. η-3 needs both. η-4 needs only η-2 (so η-3 and η-4 can land in parallel). η-5 needs η-3's subscription index + η-4's emit refactor. η-6 is independent and can ride at any point.

**Recommended serial order:** η-1 → η-2 → η-3 → η-4 → η-5 → η-6. Each PR ~1-3 days. Total clock: 1.5-2 weeks if serial; tighter if η-3/η-4 land in parallel.

**Bundling option:** η-1 + η-2 as a single "foundation" PR (smaller, all-additive, well-contained); then η-3, η-4, η-5, η-6 each their own PR.

## Tracking

- Parent plan: `docs/EVENTS_SPEC_ALIGNMENT_PLAN.md`. Update its η section with `Status: shipped via η-1..η-6` once this plan completes.
- Per sub-PR: each carries the spec citation in commit + PR description (same convention as α-ζ).
- WG demo opportunity: the kitchen-sink multi-source/multi-consumer example in `mcpcontribs/proposals/triggers-events-wg/poc-notes.md` ("kitchen-sink" backlog item) is the natural showcase for η. Worth filing a tracking issue when η-3 lands so the demo design can start in parallel.

## Tutorial follow-up

`tutorials/walkthrough/events.md` will need updates as η ships. Convention is post-implementation, not pre-doc — but tracking here so it doesn't get lost. Each sub-PR's PR description should include a "Tutorial impact" note pointing back at this section so reviewers know the doc work is queued, not forgotten.

| When this lands | events.md change |
|---|---|
| **η-1** (poll-lease) | Q2 "Three delivery modes" table: poll-mode "Statefulness" cell currently says "Client persists `cursor`; server is idempotent on re-poll." Add: "server holds an ephemeral lease keyed on `(principal, name, params)` for lifecycle-hook firing — see Q8." |
| **η-2** (hook surface) | No tutorial change in isolation — surface is invisible until η-3/η-4 wire it. Document together. |
| **η-3 + η-4** (lifecycle + match/transform — most natural to land near each other) | **Add Q8 — "How does an author shape per-subscription delivery?"** Covers: the four hooks (`Match` / `Transform` / `OnSubscribe` / `OnUnsubscribe`) as `EventDef` fields; `HookContext` fields (Principal / SubscriptionID / Mode); when each hook fires per delivery mode (table); the suspend-≠-unsubscribe rule from Q4 in this plan; hot-path discipline (sync, panic-recovery, snapshot-and-drop-lock). Update Q4 ("What's a source?") `YieldingSource` row to mention hooks fire on `yield()` fanout; document explicitly that TypedSource authors apply filtering inside their `Poll` callback (no hook plumbing). End-state bullet: add "per-subscription filtering and lifecycle are first-class via `Match` / `Transform` / `OnSubscribe` / `OnUnsubscribe` hooks on EventDef." |
| **η-5** (targeted emit) | Q1 four-knobs table is unchanged (no new wire surface). Q4 or new Q8: brief mention of `EmitToSubscription(srv, e, subID)` as the alternative to broadcast `Emit`, when authors already know the target sub from an `OnSubscribe`-provisioned listener. |
| **η-6** (TooManySubscriptions) | Q3 IMPORTANT callout currently lists "three rules" — extend to four: add the quota-before-on-subscribe rule with the `-32013` error code reference. Brief — one bullet. |
| **all of η shipped** | Header `Branches into:` already lists three planned leaves; consider adding a fourth leaf for hooks if any single piece (probably the lease semantics or the broadcast-vs-targeted decision flow) warrants its own page. Defer the call until η-4 lands and we can see what readers actually ask about. |

**Cross-page check:**
- `tutorials/walkthrough/extension-mechanisms.md` Q5 case-study row for events: currently says "experimental, target-shape, `experimental.events` capability." Stays as-is — it doesn't enumerate hook surface, only the capability. No change needed.
- `tutorials/walkthrough/request-anatomy.md`: hooks are author-side surface, not dispatch internals. No change needed unless η-3's wiring introduces a new dispatch pattern worth calling out (current design slots into existing `HandleMethod` registration; expect no change).
- `tutorials/walkthrough/notifications.md`: η doesn't touch notification mechanics. No change needed.
- `tutorials/walkthrough/INDEX.md`: events row's End-state summary will need its bullet list extended with the hook surface once η-3 + η-4 land. `make graph` regenerates GRAPH.md automatically.

**Operational reminder:** mcpkit docs work commits direct to main per the established workflow — same as the events.md initial drop. Each sub-PR can either bundle the tutorial update in the same commit (keeps doc + code in lockstep) or land them as separate same-day commits (cleaner per-file diff). Recommend the same-commit pattern for η-3/η-4 since the doc text references newly-shipped behavior literally.

## Out of scope

- **Persistent lease/registry across server restart.** Spec is explicit that this is ephemeral SDK state (L715). Don't try to durable-store it.
- **Cluster-wide hook coordination.** Spec L946-963 acknowledges multi-replica deployments need shared state but doesn't normatively require it. Document the single-process constraint in `DEPLOYMENT.md` rather than building a distributed-coordination layer.
- **Match/transform on the TypedSource path.** TypedSource owns its Poll callback; authors apply filtering there. Documented as a deliberate non-goal.
- **Endpoint verification challenge envelope (`type:verification`)** — Open Question 6 in the spec; out of scope for η, will be a follow-up if the spec resolves.
- **`SubscribeFailed` vs `TooManySubscriptions` differentiation.** η-6 maps quota failures to `-32013 TooManySubscriptions`. If on_subscribe returns an error for a non-quota reason (upstream API down, etc.), we'll need a new error code; defer until we have a concrete use case from a real source.

## How to use this plan

- Start a sub-PR: read its section, open a feature branch (`feat/events-eta-N-<theme>`), file a tracking issue if it'll need cross-WG visibility.
- Mid-PR: if scope changes, update the relevant section here in the same PR.
- After PR merges: tick the sub-PR as "shipped" by updating its Status line and the TL;DR table.
- If a design decision in the "Open design questions" section turns out to be wrong on contact with code: document the override in the relevant sub-PR's section and update the Q in this file. Don't silently change direction.
