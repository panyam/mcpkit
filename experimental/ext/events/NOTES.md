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
- **Three ids, three dedup owners.** `eventId` names an occurrence: minted once at yield
  (`evt_` + 16 random bytes, `yield.go`) or passed through from upstream via `MakeEvent`, and
  identical on every copy (poll, push, webhook, retry, backfill), so the client dedups on it. The
  webhook subscription id names a standing request: `sub_` + SHA-256 of
  `principal ␟ url ␟ name ␟ sorted(arguments)` truncated to 16 bytes (`identity.go`), so a repeat
  subscribe lands on the same row and the server dedups subscriptions. A push stream is a new
  subscription on every open, identified by its JSON-RPC id. `webhook-id` names a POST and is set
  to the `eventId` for events (`msg_<type>_<random>` for control envelopes), so a retry carries
  the same id and a Standard Webhooks receiver drops it. A random id per attempt would defeat that.
  The Go receiver does not dedup or check timestamp age yet (#1558).

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
author on 2026-09-22. His answer: the extensions map is correct and the sketch needed fixing. He
opened upstream PR 7 to fix it, and #1421 moved the implementation and the suite's rows together.
PR 7 closed unmerged on 2026-09-28, so the sketch's `main` still says top-level; SEP-3415
§ Capability Negotiation carries the rule now.

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
`-32026 Unsupported`, `data.feature: "deliveryMode"`. Before #1416 the array was decorative: poll
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

---

## Webhook signatures key on the decoded secret

`HMAC-SHA256(decoded(secret), webhook-id + "." + webhook-timestamp + "." + body)`, base64 with a
`v1,` prefix. `signingKey` in `secret.go` does the decoding, accepting both base64 alphabets because
the SDKs disagree on which they emit, and only when the `whsec_` prefix is present: an unprefixed
value is not a spec-format secret, and decoding one anyway reinterprets most short strings.

For four months everything here keyed on the literal string instead. Signing landed 2026-04-29 when
the secret was an opaque token and that was right; the `whsec_` format arrived two days later and
derivation was never revisited. Signer, both verifiers, the Go and Python clients and the
whole-enchilada receiver all agreed with each other and none agreed with the spec, so every test
passed. If you touch signing, test against the specification text rather than against this package's
own signer.

The legacy `X-MCP-Signature` mode still keys on the literal, deliberately: the spec defines
derivation for the Standard Webhooks formula only.

## `delivery` is enforced on poll and subscribe, and derived when absent

Both `registerPoll` and `registerSubscribe` refuse a mode the descriptor does not advertise, with
`-32026` and `data.feature: "deliveryMode"`. `registerStream` still gates on whether the source
implements `streamSubscribable` instead, which is a third basis for the same decision and is
tracked in #1417.

`deriveDelivery` fills in a source that declared nothing: poll always, push when the source is
`streamSubscribable`, webhook when a registry is wired. That last clause used to be nested inside
the push branch, which made every non-streamable source derive poll-only. Nothing noticed until
subscribe began enforcing, because until then the array was decoration.

**Read `Registry.Def(name)`, not `src.Def()`,** anywhere the answer must match what `events/list`
published.

## Two escape hatches, deliberately separate

`WithWebhookAllowPrivateNetworks` governs *hosts* (loopback, RFC1918, link-local, ULA).
`WithUnsafeWebhookAllowPlaintextCallbacks` governs the *scheme*. They used to be one flag because
the demos want both, and that conflation meant a fixture needing a local receiver silently stopped
enforcing https too, so a conformance run reported a demo setting as a library defect.
`examples/events/kitchen-sink` sets the plaintext hatch only outside `--conformance-events`.

---

## Endpoint verification runs inside subscribe (#490)

The handshake is synchronous. The spec says a failed one "yields `-32027`" and that the categories
appear "as `data.reason` on a `-32027 CallbackEndpointError` returned synchronously from
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
- **It emptied `events-webhook` for a day and nothing went red.** That scenario grades subscribe
  semantics against callbacks under `https://conformance.invalid/mcp-events`, which never resolves,
  so with verification on every subscribe failed and 26 of 28 rows went untestable (22/23 to 1).
  `testconf-events` is `INFO`, so CI stayed green, and #1444 was checked against the one unwired
  scenario rather than the four wired ones. kitchen-sink now allowlists that origin under
  `--conformance-events`. Run `make testconf-events` after touching anything on the subscribe path;
  an `INFO` suite only protects you if someone reads it.
- **The conformance harness's probe callbacks answer every POST with their failure status**, the
  verification POST included, so the 410/413/retry/redirect rows in `events-webhook-delivery` went
  untestable once the handshake existed. That is a suite fix, not a library one: a probe should answer
  the challenge and misbehave afterwards.

`VerifiedAt` on the stored target is the persisted half. `verifyEndpoint` checks the store before the
allowlist, so a no-expiry subscription restored after a restart refreshes without a new POST. The
in-memory cache is separate and TTL-scoped like the subscription itself.

---

## A suspended no-expiry subscription used to live forever (#1443)

Failure-based GC was decided only inside `recordDeliveryFailure`. A suspended target is skipped by
`Targets()` and `DeliverToTarget`, so it never records another failure, so its GC window was never
checked again. With the defaults (suspend after 5 failures, 72 h window) a no-expiry subscription to
a dead receiver suspended within minutes and then sat in the store indefinitely, with no TTL to
collect it either. The unit test for GC called `recordDeliveryFailure` directly, which is why it
passed: it never went through suspension.

`pruneExpiredLocked` now also collects suspended no-expiry targets past the window and drops them
through `PostTerminated`, so they get the same envelope and `onRemove`. It runs from `Register` as
before and from `Deliver`, throttled to a quarter of the window and at most once a minute
(`sweepIfDue`), because a Postgres-backed store pays a full `ListWebhooks` per sweep. A registry with
no event traffic at all still never sweeps; nothing is delivered in that state, so it costs storage
rather than traffic.

Found while wiring the conformance GC row, which is the second time a suite row has surfaced a
library bug that every unit test passed over.

---

## Path (d) shipped opt-in, and the tests had to be sharpened twice (#1436)

The receiver-published document is off unless `WithWellKnownReceiverDocs()` is passed. Its text has
been stable since the 2026-06-15 sketch commit, but it is a MAY, it sends a request to every new
callback origin, and Svix objected to what publishing it reveals. Opt-in keeps the cost of a
review-time change to one parser and one constant.

Mutation testing caught three tests that could not fail. The origin was built as `https://`
regardless of the URL's scheme, so the scheme check was unobservable (a plaintext origin just failed
TLS); it is built from the real scheme now. The oversized-document fixture was invalid JSON once
truncated, and the redirect fixture carried an HTML body, so both were rejected by the parser before
the size and status checks mattered; both fixtures now carry a valid document. Worth doing to any
"falls back to X" test: make the bad input otherwise good, or the fallback proves nothing about the
check it names.

## Source signals reach webhooks through a registry hook (#1466, #1468)

`YieldGap` and `YieldTerminated` used to fan out to push streams only. Webhook subscribers heard
nothing, so a source that ended left its webhook subscriptions registered against a name that would
never emit again. `Register` now installs a signal hook next to the emit hook (`wireLocked`), and the
hook routes a gap to `PostGapByEventName` and termination to `TerminateByEventName`.

Two things about it that are easy to break:

- **The hook runs after `s.mu` is released.** `Latest()` takes the lock for read, and terminating
  fires `onRemove` hooks (quota release) that must not run under the source's lock. `YieldTerminated`
  lost its `defer` unlock for this reason.
- **"No position" is decided on the wire, in both modes.** `PostGap` sends nothing for an empty
  cursor, and the stream handler sends no fresh `active{truncated:true}` when `Latest()` is empty
  or the source is cursorless. `SubscriberEvent.Truncated` itself is unchanged, since a custom
  `Subscribe` consumer may still want to know events were lost.

The tutorial's signal table had claimed for months that `YieldTerminated` already sent webhooks a
`terminated` envelope "via `postTerminatedSilent`". It never did: that function is the suspend path.
Nobody noticed because nothing exercised it; the conformance fixture reached webhooks per
subscription instead. If a doc describes wiring, check it against the code before relying on it.

## The Go client reconnects a silent stream, and measures the interval to do it (#1503)

SEP-3415 says a client that has heard neither an event nor a heartbeat for more than twice the
heartbeat interval SHOULD treat the stream as dead and reconnect with its cursor. `eventsclient.Stream`
used to make one `events/stream` call and wait on it forever, so a stream that died behind a proxy
that never closed the connection just went quiet. The client conformance scenario caught it
(`events-client-push` 0/1).

The interval is never announced, which is the awkward part. `Stream` measures it as the largest gap
between consecutive heartbeats, and assumes the SEP's 30-second maximum until two have arrived, so an
uncalibrated stream is declared dead after a minute. Largest gap rather than latest, because bursts of
events can shorten the gaps you happen to see and a reconnect storm is worse than a slow detection.

What a reconnect sends: the last cursor any frame delivered (active, heartbeat or event), and an
explicit `cursor: null` for a stream whose frames all carried null. Never a cursor from before the
null, which would replay from a position the server said doesn't exist. What ends the stream instead
of reconnecting: `Stop`, `notifications/events/terminated`, a normal final result, or a reconnect
the server answers with a JSON-RPC error (an answer, not an outage, so it surfaces through `Err()`).
Transport failures back off from 250ms to 30s.

It is on by default, because the SEP makes it a SHOULD and the conformance driver may only call what
the SDK offers. `StreamOptions.DisableReconnect` restores the single call.

## `list_changed` moved onto subscriptions/listen (#1505)

SEP-3415 delivers `notifications/events/list_changed` "only on a `subscriptions/listen` stream, and
only when the client opted in", through the filter key `eventsListChanged`, and the acknowledgement
echoes the key when the server declared `listChanged: true`. `Register` now registers that key with
#1524's `server.ListChangedFilterKey`. `broadcastListChanged` did not change: a broadcast reaches a
listen stream only when the stream's filter matches, and the legacy session wire still gets it on the
GET stream, so one call serves both.

On the client side, `eventsclient.WatchListChanged` opens a listen stream for just that key, and
`PollOptions.OnListChanged` is deprecated, since a conformant server never sends the notification on
a poll response. The conformance driver used to rely on exactly that, because the suite's mock sent
it there. The suite moved too.

Grading the server half needed the catalog to change during a run, which a healthy server never
does. `events_conformance_catalog` on kitchen-sink adds and removes types (the add/remove half of
#1465), minting a fresh `catalog.N` each time for the same reason `build.finished` exists.


## The Go client dedups on eventId, and the receiver scopes it per subscription (#1558)

The server mints `eventId` once at yield and every copy carries it, and `webhook-id` on an event
POST is that same id. Nothing on the client used it, so a webhook retry or a poll backfill that
overlapped push delivery reached the application twice. That was harmless for a dashboard and
expensive for the bridges in #1579 and #1581, where each delivery starts an agent turn.

The seen-set is a value (`Deduper`) rather than a field of each loop because the overlap that
matters crosses delivery modes: a stream's push frames and the poll backfill after it. A private
set per call catches retries only.

The receiver keys on `X-MCP-Subscription-Id` plus `eventId`, not `webhook-id` alone. One event
matching two subscriptions to the same URL arrives as two POSTs with the same `webhook-id`, and
whether SEP-3415 means that to be one delivery or two is an open question for the WG. Scoping by
subscription delivers both, which is the reversible choice: a caller can collapse them, but cannot
recover one that was dropped. A side effect is that sharing a `Deduper` between a `Receiver` and a
`Poll` dedups nothing across them when the POST carries a subscription id, which the README says.

A delivery dropped on a full channel is forgotten again, so the server's retry is delivered.

The verifiers in `headers.go` check only the HMAC, so a captured POST verified forever. Rather than
change their signatures, `TimestampWithinTolerance` sits beside them and the receiver calls both,
with five minutes as in the Standard Webhooks reference libraries.

## The events bridge gives each watch its own callback URL (#1579)

`clients/go/bridge` subscribes by webhook with itself as the receiver. A delivery has to be matched
to the watch that asked for it, so the instruction and cursor can be found. `Receiver` does not hand
the application the `X-MCP-Subscription-Id` header, and two watches on one event with different
arguments are separate subscriptions. So each watch gets its own path (`/hooks/<watch id>`), its own
secret and its own `Receiver`, and the URL does the routing. A side benefit is that a leaked secret
signs deliveries for one rule only.

The secret is stored and reused on resubscribe. The server derives a subscription's id from
principal, name, arguments and URL, so a restart that sends the same URL and secret refreshes the
existing subscription rather than creating a second one. A fresh secret on restart would also fail
endpoint verification against a receiver still holding the old one.

The cursor advances after the sink returns, not when the POST arrives. A crash mid-run then replays
that event on restart instead of losing it, and the shared `Deduper` (#1558) does not survive the
restart, so a sink can see one event twice across a crash. That is the safe direction for a
standing rule, but a sink with side effects should be idempotent on `eventId`.

Building it found `Receiver.Close` racing an in-flight POST: the closed check and the channel send
were not under one lock, so a delivery arriving during `unwatch` panicked with "send on closed
channel". The send now happens under the lock `Close` takes.

SEP-2495 (event-driven re-entry) has only its motivation written, so there was no wire design to
follow. The bridge's answer is that re-entry is a new run with the stored instruction, not a turn
in the session that subscribed, because a standing rule cannot depend on that session being open.
