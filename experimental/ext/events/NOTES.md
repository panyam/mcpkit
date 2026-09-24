# experimental/ext/events — implementation notes

For the API and the tracing story see `README.md` and `docs/SEP_414_OTEL.md` § Events bus trace
context relay / § Events fanout span emission.

This package is **not in the per-PR `test.yml` matrix**; it runs via the experimental umbrella and
`make testall` only.

---

## Spec-alignment sweep (PRs 778 / 779 / 783 / 786)

These bite together when touching this package.

### `params` → `arguments` was wire-breaking *and* Go-field-renaming (PR 778)

The JSON tag flipped on all four request structs (`events/subscribe`, `/poll`, `/unsubscribe`,
`/stream`) **and** the Go field was renamed `Params` → `Arguments`.

That second half was deliberate, since a compiler-driven sweep catches every call site where a
grep-only rename would silently leave sub-modules behind. Touched `RegisterParams.Arguments`,
`WebhookTarget.Arguments`, `SubscribeOpts.Arguments`, the Go SDK's `SubscribeOptions.Arguments` and
`StreamOptions.Arguments`, and the GORM `webhookRow.Arguments` column.

Internal canonical-key computation uses the Go field, so derived IDs are stable across the rename.
Pinned by `TestCanonicalKey_StableAcrossRename`.

### `WebhookTarget.ExpiresAt` is `*time.Time` (PR 779)

Nil is the no-expiry sentinel. **Every call site that touches expiry must guard nil**: the prune
loop (`pruneExpiredLocked`), the `Targets()` filter, the `DeliverToTarget` liveness check, and the
`ExpireAll` test helper. External `WebhookStore` implementors must update too. The GORM column is
nullable.

### The `ttlMs` tristate decode

Go's `*int64` collapses absent and JSON-null to the same nil, so the `events/subscribe` handler
decodes `ttlMs` as `json.RawMessage` and pattern-matches: empty bytes is absent, literal `"null"`
is a no-expiry request, otherwise parse int64.

`WebhookRegistry.NegotiateExpiry(rawTTLMs json.RawMessage)` is the policy oracle: handler-private
decode, clamp to `[MinWebhookTTL, MaxWebhookTTL]`, honor `WithUnsafeWebhookTTLBypass`, and gate
null acceptance behind `WithAllowInfiniteWebhookTTL`.

**Per spec there is no rejection path for TTL values**, so malformed input collapses silently to the
server default.

`refreshBefore` is `*time.Time` everywhere and is always present on the wire (RFC3339 for finite,
JSON `null` for no-expiry). The Go SDK's `Subscription.RefreshBefore()` returns `*time.Time`; nil
signals no-expiry, and the refresh loop drops to a 1-hour health-check cadence in that case,
because clients should still re-subscribe occasionally for cursor advancement and
`deliveryStatus` observation.

### `WithAllowInfiniteWebhookTTL()` is policy, not plumbing

Default off. Without it, `ttlMs: null` collapses to the server default.

Operators flipping it on **without** `WithWebhookStore(persistent)` get a stark warning at
construction (`warnIfInfiniteTTLWithDefaultStore`): no-expiry subscriptions in the in-memory store
violate the spec's "MUST persist across restarts" obligation. It warns rather than rejects, mostly because
dev and test setups may legitimately opt in.

---

## Three distinct cleanup state machines on `WebhookRegistry`

Non-overlapping thresholds, non-overlapping actions. Do not merge them.

| Machine | Applies to | Action |
|---|---|---|
| TTL prune | finite-TTL only (no-expiry exempted per PR 779) | remove at expiry |
| Suspend | all subscriptions | sliding-window consecutive failures; **reversible** on refresh; fires a silent `terminated` envelope on transition |
| Failure-based GC (PR 783) | no-expiry only | continuous failure for `noExpiryFailureGCWindow`; **irreversible** drop; fires PostTerminated with a distinguishable message |

**`FailedSince` vs `FailingContinuouslySince`** are two anchors for two paths, and confusing them
breaks both:

- `FailedSince` resets by sliding `suspendWindow`. Correct for finite-sub suspend, where refresh
  reactivates.
- `FailingContinuouslySince` is **never** reset by a quiet period, only by a successful delivery
  (`recordDeliverySuccess` clears it).

Failure-GC trigger: `target.ExpiresAt == nil && now - *FailingContinuouslySince >
r.noExpiryFailureGCWindow`. `DefaultNoExpiryFailureGCWindow = 72h`; the demo overrides to 2m via
`EVENTS_NO_EXPIRY_GC_WINDOW`. Wire-projected as an RFC3339 diagnostic
(`deliveryStatusForResponse`) so subscribers can observe the anchor.

---

## Delivery semantics

- **410 Gone is abandon-without-failure.** The retry loop has a `case http.StatusGone:` branch
  **before** the generic 4xx catch-all. It returns without calling `recordDeliveryFailure`:
  `DeliveryStatus.Active` stays true, `LastError` stays `DeliveryErrorNone`, the subscription is
  untouched. The receiver said "skip this one", not "I am broken".
- **`DefaultWebhookAckTimeout = 5 * time.Second`**, one named constant used by both the
  `http.Client` and the `net.Dialer`. No `WithWebhookAckTimeout` option until a concrete need
  appears.
- **`DeliveryStatus.Throttled` and `RetryAfterMs *int64` are projector-only.** The wire shape
  exists but nothing in mcpkit sets them; adopters wire them from their own throttle state. The
  spec distinguishes active rate-limiting from failure-driven suspension (`Active=false`).

---

## Schema migrations: fresh deploys only

Across PRs 778 / 779 / 783, GORM column adds and renames intentionally ship **without** migration
recipes. Operators recreate the DB, and the in-code comment cross-references the PR. Documented in
`DEPLOYMENT.md`.

---

## Test-sweep gotcha

A regex-based search/replace on `r.deliver(...)` call sites **breaks** when the arguments contain
nested parens like `r.deliver(r.Targets()[0], ...)`. Use a paren-counting script rather than a
regex for any mechanical signature sweep across test files.

---

## Live trace verification without Grafana

`curl http://localhost:3200/api/search?tags=service.name=X&limit=N` for trace IDs, then
`curl http://localhost:3200/api/v2/traces/<id>` for the full span tree. Proves spans land in Tempo
with the right attributes without needing the UI.

---

## The capability declares through the extensions map, and briefly did not

`capabilities.extensions["io.modelcontextprotocol/events"]`, via `EventsExtension` and
`srv.RegisterExtension`, exactly like `ext/skills`, `ext/tasks` and `experimental/ext/agents`.

It spent a week at the top level of `capabilities` instead. The merged design sketch specified that,
#1416 implemented it, and `metronome-mcp.fly.dev` — written by the sketch's author — used the
extensions map. The conformance suite therefore graded one of the two wrong whichever way it read.
Rather than pick, or declare in both places and make everyone pass, the disagreement went to the
author on 2026-09-22. His answer: the extensions map is correct and the sketch needed fixing, which
is upstream PR 7. #1421 moved the implementation and the suite's rows together.

Worth keeping for the next time a document and a reference implementation disagree: following the
document is what made the question visible. Declaring in both places would have been the
accommodating choice and would have left the sketch saying the wrong thing indefinitely.

`ListChanged` is true whenever `Register` wires the handlers, because `AddSource` and `RemoveSource`
broadcast unconditionally. The spec reads the flag as a promise rather than a hint — a server
declaring false must not send the notification — so a future option that suppresses the broadcast has
to move this flag with it. A false `ListChanged` serializes as `{}` rather than
`{"listChanged": false}`, since the spec defines false as the default and reads an empty object as
support without list-change notifications.

## `delivery` is a contract, and the registry owns the resolved answer

An `EventDef.Delivery` that lists push and webhook means `events/poll` refuses that type with
`-32014 Unsupported`, `data.feature: "deliveryMode"`. Before #1416 the array was decorative: poll
answered for anything registered, so a client reading the descriptor to decide what to call was
reading a promise nothing kept.

Enforcement forced two design choices worth knowing before touching this:

**A declared array is never widened or narrowed; an absent one is derived, not defaulted.**
`normalizeDelivery` returns an author's list untouched. For a source that declared none it calls
`deriveDelivery`, which reports poll unconditionally (`Poll` is on the `EventSource` interface),
push when the source implements `streamSubscribable`, and webhook when push is available and a
`WebhookRegistry` is wired. Defaulting to all three would have put the same lie one layer further
in, claiming push for a `TypedSource` that cannot stream. Strict enforcement with no default would
have broken 68 of the 121 `EventDef` literals in this package's own tests, which leave the field
unset.

**The resolved value lives in the registry, not on the source.** `EventSource` is an interface and
`Def()` may build a fresh struct per call, as several test doubles do, so there is nothing stable to
write back to. `Registry.delivery` holds it and `Registry.Def(name)` is how everything else reads
it. **Read `reg.Def(name)`, not `src.Def()`, anywhere the answer has to match what `events/list`
published.**

Poll and stream still gate on different bases: stream asks whether the source implements
`streamSubscribable`, poll asks what the descriptor advertises. They agree today only because the
derivation uses the same type assertion, and they diverge the moment an author declares explicitly.
Tracked in #1417, along with webhook having no gate at all.

---

## `events` is always an array, and `events/list` is always sorted

Two wire-shape rules that each hid behind something else.

`pollResultWire.Events` carries no `omitempty`, **and** `MarshalJSON` normalizes a nil slice to
`[]`. Dropping the tag alone is not enough, since a nil slice marshals to `null`, which fails the
same client loop an absent key fails. The handler also allocates, so the marshaller is defence for
future producers rather than the thing the conformance check exercises.

`snapshot()` sorts by name. It used to range the map, and the flapping order stayed invisible
because the one source a client could not poll was never a selection candidate; giving
`events.topology` a delivery array made the order load-bearing and conformance scenarios began
picking a different event type per run. Same bug as #1408 on the apps bridge, now constraint C10.

## Endpoint verification runs inside subscribe (#490)

The handshake is synchronous. The spec says a failed one "yields `-32015`" and that the categories
appear "as `data.reason` on a `-32015 CallbackEndpointError` returned synchronously from
`events/subscribe`", so the error has to come back on that call. The asynchronous alternative (register
inactive, verify in the background, report through `deliveryStatus`) would need a pending state the
registry does not have, and a failure would only surface on the next refresh.

What that costs, and what bit us while turning it on:

- **The receiver must be serving before it subscribes.** `examples/whole-enchilada/events/webhook`
  bound its listener, subscribed, and only then called `Serve`. The TCP connect succeeds against the
  backlog and the handshake then waits out the 5 s client timeout. It now generates the secret, starts
  serving, and subscribes with `SubscribeOptions.Secret`.
- **A receiver that learns the secret after subscribe returns cannot verify the challenge.**
  `eventsclient.Receiver` constructed with `""` accepts anything, which is why the
  `NewReceiver("")` then `SetSecret(sub.Secret())` pattern in the demos still works. One constructed
  with a fixed secret the subscription does not use now refuses the challenge, correctly.
- **Default-on broke 41 package tests and every example e2e test**, none of them about verification.
  The shared stacks (`buildAuthGateStackWithOpts`, `buildSecretValidationStack`, …) now pass
  `WithUnsafeSkipEndpointVerification()`, the same way they already pass the private-network hatch.
  `buildVerifyingStack` in `verification_test.go` is the one that leaves it on. The client module
  allowlists its dead `http://localhost:1/sink` instead, so its real receivers still handshake.
- **The discord walkthrough had a success step aimed at `http://localhost:1/sink`.** No test runs
  walkthrough steps, so only running the demo found it. Run the affected demos by hand after touching
  subscribe; `make test-examples` is in no workflow (#1431).
- **The conformance harness's probe callbacks answer every POST with their failure status**, the
  verification POST included, so the 410/413/retry/redirect rows in `events-webhook-delivery` went
  untestable once the handshake existed. That is a suite fix, not a library one: a probe should answer
  the challenge and misbehave afterwards.

`VerifiedAt` on the stored target is the persisted half. `verifyEndpoint` checks the store before the
allowlist, so a no-expiry subscription restored after a restart refreshes without a new POST. The
in-memory cache is separate and TTL-scoped like the subscription itself.
