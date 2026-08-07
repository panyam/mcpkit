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

That second half was deliberate: a compiler-driven sweep catches every call site, where a
grep-only rename silently leaves sub-modules behind. Touched `RegisterParams.Arguments`,
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

**Per spec there is no rejection path for TTL values** — malformed input collapses silently to the
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
violate the spec's "MUST persist across restarts" obligation. It warns rather than rejects, because
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
