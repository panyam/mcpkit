# Plan: Events spec alignment δ — request shape alignment

**Issue:** mcpkit 349 (umbrella) | **Group:** δ (fourth of 7)
**Branch:** `feat/events-delta-wire-shapes` (from main, post-γ-merge)
**Depends on:** α (#353), β (#355), γ (#356) — all merged
**Unblocks:** ε (push delivery uses the `maxAge` plumbing δ adds; `_meta` fields δ defines surface in push notifications too)

**Spec snapshot:** [`proposals/triggers-events-wg/spec-snapshots/design-sketch-2026-04-30.md`](https://github.com/panyam/mcpcontribs/blob/main/proposals/triggers-events-wg/spec-snapshots/design-sketch-2026-04-30.md). Citations below in the form `(§"Section" L<line>)`.

## Summary

Wire-format audit. No new methods, no new behaviors — just bring every byte of the request/response JSON onto the spec's exact shape so we can interoperate with other conformant implementations (TS SDK, etc.).

Five deltas:
1. **Flat `events/poll` request** — drop the `subscriptions[]` wrapper; lift `name`, `params`, `cursor` to top level.
2. **`maxAge` replay floor** — optional integer-seconds cap on backfill, accepted on poll + subscribe (stream is ε territory).
3. **`EventOccurrence._meta`** — add the optional opaque-metadata field that matches the Tool/Resource/Prompt pattern.
4. **`EventDescriptor._meta`** — same at the event-type-definition level.
5. **`events/list` `nextCursor`** — pagination consistency with `tools/list` / `resources/list`.

Risk profile is **lower than γ**. Only one wire-breaking change (the flat poll request); no auth surgery; no identity model changes.

## Spec deltas addressed

| # | Delta | Spec citation |
|---|---|---|
| 1 | `events/poll` request: drop `subscriptions[]` wrapper. Top-level `{name, params?, cursor?, maxAge?, maxEvents?}`. | §"Poll-Based Delivery" → "Request: events/poll" L139-149 |
| 2 | All three modes (poll, stream, subscribe) accept optional `maxAge` (integer seconds). Server begins replay from `max(cursor, now - maxAge)`. Stream lands in ε; δ wires poll + subscribe. | §"Cursor Lifecycle" → "Bounding replay with maxAge" L529 |
| 3 | `EventOccurrence` field set audit: `eventId, name, timestamp, data, cursor` (already there post-α/β/γ). Add optional `_meta` opaque object. | §"EventOccurrence schema" L180-186; `_meta` from spec follow-on commit `d4faef9` 2026-05-01 |
| 4 | `EventDescriptor` (per-event-type def in `events/list`): add optional `_meta` field. Matches Tool/Resource/Prompt pattern. | Spec follow-on commit `d4faef9` 2026-05-01 |
| 5 | `events/list` response: optional `nextCursor` for pagination. Matches `tools/list` / `resources/list`. | Spec follow-on commit `d4faef9` 2026-05-01 |

The May-1 follow-on items (3 `_meta`, 4 `_meta`, 5 `nextCursor`) are pre-cached in the snapshot via the "Major changes" header at the top of the snapshot file but aren't in the body line-numbered yet — line refs in code comments will say `(spec follow-on 2026-05-01)`.

## Client impact

Most δ changes give clients new **optional** knobs, not forcing client work. One forcing change (flat poll request) — mechanical, all call sites are ours.

### Go SDK (`experimental/ext/events/clients/go/`)

| Change | Site | New surface |
|---|---|---|
| `maxAge` on subscribe (δ-3) | `subscription.go` | `SubscribeOptions.MaxAge time.Duration` field. Sent on every refresh. Zero = no floor. |
| `Event[Data].Meta` (δ-4) | `event.go` | Typed `Event[Data]` gains `Meta map[string]any`. `Receiver[Data]` decoder threads it through automatically. |
| Flat poll request (δ-1) | n/a | Go SDK has no typed `Poll(...)` helper today; callers use raw `c.Call("events/poll", ...)`. So no SDK API change, but demo test call sites flatten. |
| `nextCursor` on events/list (δ-5) | n/a | No typed `ListEvents(...)` helper either. Server-side change only. |

### Python SDK (`experimental/ext/events/clients/python/events_client.py`)

More affected because it has CLI subcommands that issue raw RPC calls:

| Change | Site | New surface |
|---|---|---|
| Flat `events/poll` request (δ-1) | `cmd_poll` + `cmd_list`'s opportunistic poll | Send `{name, cursor, maxEvents}` instead of `{subscriptions: [{...}]}`. |
| `maxAge` on subscribe (δ-3) | `WebhookSubscription.__init__` | New `max_age=0` kwarg. Optional `--max-age` CLI flag on the `webhook` subcommand. |
| `maxAge` on poll (δ-2) | `cmd_poll` | Optional `--max-age` CLI flag, sent in the poll request. |
| `Event._meta` (δ-4) | webhook receiver decoder | Threads `_meta` through to the printed event output. |
| `nextCursor` on events/list (δ-5) | `cmd_list` | Loop on `nextCursor`. Today fires once since servers don't paginate. |

### Application consumers (discord + telegram demos)

| Change | What demos do |
|---|---|
| Flat poll request | `e2e_test.go` raw `c.Call("events/poll", ...)` sites flatten. ~6 sites total. |
| `maxAge` | Optional — demos don't *need* it. Could add a small step to the walkthrough demonstrating the floor; defer unless trivially small. |
| `_meta` | Optional — demos don't currently set metadata. Could show one source attaching `_meta` to demonstrate the pattern; defer unless useful. |

### Forcing-vs-optional summary

- **Optional**: `maxAge` (poll + subscribe), `_meta` (Event + EventDescriptor), `nextCursor` (events/list). Existing client code without them continues to work; the server treats absence as "default behavior".
- **Forcing**: flat `events/poll` request shape. Old `{subscriptions: [...]}` requests get `-32602` with a spec-pointing message. We own all call sites.

## Why these deltas matter (brief)

- **Flat poll request**: interop. TS SDK + future implementations parse the spec's flat shape directly; our wrapper means anyone hitting our server with the spec shape gets a parse error. Wire-breaking but the spec is firm.
- **`maxAge`**: bounds backfill on long-offline reconnects. Without it, a client offline for a week reconnects with an old cursor and the server may try to replay a week of events. With `maxAge: 300`, worst case is 5 minutes of backfill.
- **`_meta` (Event + EventDescriptor)**: app-level metadata that doesn't fit `data`. Matches Tool/Resource/Prompt — uniform pattern across MCP. The library threads it through; spec doesn't define semantics.
- **`nextCursor`**: pagination for servers with many event types. Most servers won't emit it, but plumbing it through keeps `events/list` consistent with `tools/list` / `resources/list` for clients that already know how to paginate.

## Commits

5 atomic commits, ordered to keep each green at HEAD.

### δ-1 — flat `events/poll` request shape

Drop the `subscriptions[]` wrapper at the JSON-RPC params level. The internal `pollResultWire` response shape is already flat (α work); δ-1 mirrors that on the request side.

**Wire change:**

```jsonc
// before
{"subscriptions": [{"name": "discord.message", "cursor": "0"}]}

// after
{"name": "discord.message", "cursor": "0"}
```

**Files**:
- `experimental/ext/events/events.go` — `registerPoll` request struct flattens. Drops the multi-sub guard (single-sub is the only shape the spec defines now). Returns `-32602 InvalidParams` if the legacy wrapper appears (helpful error for old clients).
- `experimental/ext/events/clients/python/events_client.py` — `cmd_poll` sends the flat shape; `cmd_list`'s opportunistic poll too.
- `examples/events/discord/e2e_test.go` + `examples/events/telegram/e2e_test.go` + `examples/events/telegram/handlers_test.go` — every `c.Call("events/poll", {"subscriptions": [...]})` site flattens.

**Tests**:
- `TestPoll_FlatRequestShape` (NEW in `wire_shape_test.go`) — sends flat request, asserts success.
- `TestPoll_RejectsLegacyWrapper` (NEW) — sends `{"subscriptions": [...]}`, asserts `-32602` with a message pointing at the spec change.

**Spec cites for code comments**: `(§"Poll-Based Delivery" → "Request: events/poll" L139-149)`

### δ-2 — `maxAge` on poll + filtering in YieldingSource

Wire `maxAge` (integer seconds) through `events/poll`. `YieldingSource.Poll` filters out events with `timestamp < now - maxAge`.

**Files**:
- `experimental/ext/events/events.go` — `registerPoll` request struct adds `MaxAge int`. Pass to `source.Poll(cursor, maxEvents, maxAge)`.
- `experimental/ext/events/yield.go` — `Poll(cursor string, limit int, maxAge time.Duration)` — new param. After buffer scan, drop events older than `now - maxAge` (when set). If filtering caused the result to start later than `cursor`, set `Truncated: true`.
- `experimental/ext/events/clients/go/subscription.go` — already plumbs maxAge for subscribe in δ-3; poll doesn't go through the SDK so no change.

**Tests**:
- `TestYieldingSource_MaxAgeFilters` (NEW in `yield_test.go`) — yield 3 events, sleep 2s, yield 2 more, poll with `maxAge: 1s`, assert only the latest 2 returned + `Truncated: true`.
- `TestYieldingSource_MaxAgeZeroNoFilter` — `maxAge: 0` means "no floor"; all events returned.
- `TestPoll_MaxAgeRequest` (NEW in `wire_shape_test.go`) — request with `maxAge`, server filters correctly.

**Spec cites**: `(§"Cursor Lifecycle" → "Bounding replay with maxAge" L529)`

### δ-3 — `maxAge` on `events/subscribe`

Subscribe accepts `maxAge`; passes through as a per-subscription replay floor for the initial subscribe and any refresh re-subscribes (server treats it the same way poll does — caps replay scope).

**Files**:
- `experimental/ext/events/events.go` — `registerSubscribe` request struct adds `MaxAge int`. Stored on `WebhookTarget` for replay decisions on (future) reconnect — for δ this is just plumbing; current webhook delivery doesn't replay (that's stream + connection-recovery territory).
- `experimental/ext/events/webhook.go` — `WebhookTarget` gains `MaxAgeSeconds int` field. Register/Unregister signatures unchanged (canonical key is still `(principal, url, name, params)` — `maxAge` is metadata, not identity).
- `experimental/ext/events/clients/go/subscription.go` — `SubscribeOptions.MaxAge time.Duration` (zero = no floor). Sent on every refresh.
- `experimental/ext/events/clients/python/events_client.py` — `WebhookSubscription.__init__` gains `max_age` kwarg.
- Demos — optional `--max-age` flag in the python helper for the `webhook` subcommand.

**Tests**:
- `TestSubscribe_AcceptsMaxAge` (NEW) — subscribe with `maxAge: 300`, no error, target stored with maxAge=300.
- `TestSubscribe_DefaultsMaxAgeToZero` — omitted `maxAge`, target stored with maxAge=0 (no floor).

**Spec cites**: `(§"Cursor Lifecycle" → "Bounding replay with maxAge" L529)`. Stream support deferred to ε.

### δ-4 — `_meta` on `EventOccurrence` + `EventDescriptor`

Two parallel additions of an optional opaque `_meta` field. Matches the pattern across `Tool` / `Resource` / `Prompt` in base MCP.

**Files**:
- `experimental/ext/events/events.go` — `Event` struct gains `Meta map[string]any \`json:"_meta,omitempty"\``. `EventDef` gains `Meta map[string]any \`json:"_meta,omitempty"\``.
- `experimental/ext/events/yield.go` — `MakeEvent` doesn't need a Meta param; authors who want metadata set it post-construction or via a new variant. Keep the old `MakeEvent` signature unchanged for back-compat; consider `MakeEventWithMeta` if usability needs it (defer for now).
- `experimental/ext/events/clients/go/event.go` — typed `Event[Data]` gains `Meta map[string]any` field.
- `experimental/ext/events/clients/python/events_client.py` — receiver decoder threads `_meta` through.

**Tests**:
- `TestEvent_MetaSerializesWithUnderscorePrefix` (NEW in `cursorless_test.go` or new `meta_test.go`) — Event with Meta marshals to JSON containing `_meta` (not `meta`).
- `TestEvent_MetaOmitsWhenAbsent` — Event without Meta marshals without the `_meta` key.
- `TestEventDef_MetaInListResponse` — events/list response includes `_meta` for sources that set it.

**Spec cites**: `(spec follow-on commit d4faef9 2026-05-01)`. The May-1 spec follow-on isn't body-line-numbered in the cached snapshot; cite by commit SHA.

### δ-5 — `nextCursor` on `events/list` response

Optional pagination cursor matching `tools/list` / `resources/list`. δ doesn't implement actual pagination logic (event lists are small in our demos) but threads the field through so future servers can.

**Files**:
- `experimental/ext/events/events.go` — `registerList` response shape gains `nextCursor string \`json:"nextCursor,omitempty"\``. Today always omitted (we return all sources in one response). Authors who need pagination subclass / wrap the handler — out of δ scope.
- Python SDK `cmd_list` reads `nextCursor` from the response; loops if non-empty. Today the loop runs once and `nextCursor` is empty.

**Tests**:
- `TestList_ResponseShape` (NEW or updated) — events/list response includes the `events` array, omits `nextCursor` (we don't paginate today). Pin shape so future pagination doesn't accidentally break the omitted-when-empty contract.

**Spec cites**: `(spec follow-on commit d4faef9 2026-05-01)`.

## Files NOT touched (intentionally)

- `experimental/ext/events/headers.go` — no signature/header changes in δ.
- `experimental/ext/events/identity.go` — γ's tuple identity stays as-is. `maxAge` is metadata, not identity.
- `experimental/ext/events/webhook.go` deliver path — δ doesn't change how deliveries go out, only what fields they may carry (`_meta` if author set it).

## Tests

### Red-before-green

Each new test would fail against current HEAD because:
1. Flat poll request — server expects `subscriptions[]`; sending the spec shape fails with "subscriptions[] must contain exactly one entry"
2. `maxAge` filtering — `Poll(cursor, limit)` doesn't accept maxAge; events past the floor get returned
3. `_meta` on Event — struct field doesn't exist; reflection-based marshal would skip; tests would fail
4. `nextCursor` on events/list — response doesn't include the field; assertions for its presence (when set) would fail

Each test added ships green AFTER the corresponding code change in the same commit.

### Existing test updates

- All `c.Call("events/poll", {"subscriptions": [...]})` sites in demo e2e tests — flatten in δ-1.
- `pollResult` struct in demos — already flat (α work). No change needed.

### Assertion delta tracking

- Added: ~10 new tests across 5 commits
- Updated in-place: ~6 existing demo poll-call sites (request shape only)
- Weakened: 0

## Constraints check

| Constraint | Status |
|---|---|
| No CONSTRAINTS.md in this project | OK |
| No globals | OK — `maxAge` is per-request / per-subscription |
| No new abstractions for hypothetical futures | OK — `_meta` and `nextCursor` are spec-defined; not speculative |
| No backwards-compat shims | OK — `subscriptions[]` wrapper is rejected loudly with helpful error |
| Single-delta-per-commit | OK — 5 commits, 5 deltas |
| Spec citation in commits + code | OK — citations inline per the convention |

## Acceptance criteria

- `cd experimental/ext/events && go test -count=1 ./...` green
- `cd experimental/ext/events/clients/go && go test -count=1 ./...` green
- `cd examples/events/discord && go test -count=1 ./...` green
- `cd examples/events/telegram && go test -count=1 ./...` green
- Wire test: an `events/poll` request body with `name` at top level (no `subscriptions[]`) is accepted; the legacy wrapper returns `-32602` with a spec-pointing message
- Wire test: `events/poll` with `maxAge: 300` filters out events older than `now - 300s`
- Wire test: `Event` struct marshaled to JSON includes `_meta` only when set; key spelled `_meta` (underscore prefix)
- Wire test: `events/list` response shape includes `nextCursor` field with `omitempty` (not present when empty)
- Manual: discord demo `make demo` runs end-to-end; the python `make poll` works against the new flat request shape

## Out of scope for δ (explicitly)

- **Push delivery (`events/stream`)** — ε. Stream's `maxAge` handling lands there.
- **Reconnect-with-maxAge** semantics for webhook (server-side replay queue with maxAge filtering on resume) — ζ; depends on the suspend/reactivate state machine ζ adds.
- **Actual pagination logic** for `events/list` (computing `nextCursor`, accepting it on requests) — application-layer concern; δ just threads the field through.
- **`MakeEventWithMeta` SDK convenience constructor** — author can set `event.Meta` directly post-construction; defer the helper until a clear use case appears.

## Risk

**Low**. No control flow changes. The wire-breaking change (flat poll request) is mechanical; demo + SDK callers are the only consumers and they're updated in the same commits.

The `maxAge` filtering in `YieldingSource` is the only meaningful new behavior — small change (filter slice after buffer scan), well-tested.

## Done when

- All 5 commits land
- PR opened linking #349
- PR merged
- #349 updated with δ merged ✅
- Root `PLAN.md` retired (ε writes its own)
- Manual verify: discord demo `make demo` works; python `make poll` works against the flat shape

## Spec citation in commits

Same convention as γ — every commit body carries `(§"Section" L<line>)` citations. The May-1 follow-on items (#3, #4, #5) cite by upstream commit SHA `d4faef9` since they aren't body-line-numbered in the cached snapshot yet.
