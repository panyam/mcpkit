# Plan: Events spec alignment α — wire renames + error code remap

**Issue:** mcpkit 349 (umbrella) | **Group:** α (first of 7)
**Branch:** `feat/events-alpha-renames` (from main)
**Depends on:** nothing
**Unblocks:** β (secret-mode collapse), γ (auth + tuple identity)

## Summary

Pure-mechanical renames that move our events wire bytes onto the spec's vocabulary. No new behavior, no semantic changes. Three small wire-shape fixes plus an error-code remap. Intentionally narrow — single delta per commit.

## Four deltas

### 1. `cursorGap` → `truncated`

Rename the field on poll responses, the Go type, the Python decode path, and the README. The spec uses one signal across modes; we currently use `cursorGap` on poll only.

| Site | Before | After |
|---|---|---|
| `events.go:230` (`pollResultWire`) | `CursorGap bool \`json:"cursorGap,omitempty"\`` | `Truncated bool \`json:"truncated,omitempty"\`` |
| `events.go:323` (handler emit) | `CursorGap: pr.CursorGap,` | `Truncated: pr.Truncated,` |
| `events.go:78,86` (PollResult struct) | `// CursorGap ...` + `CursorGap bool` | `// Truncated ...` + `Truncated bool` |
| `yield.go:177` | `return PollResult{... CursorGap: gap}` | `return PollResult{... Truncated: gap}` |
| `yield_test.go:81-97` | `TestYieldingSource_EvictionAndCursorGap` + `pr.CursorGap` | `TestYieldingSource_EvictionAndTruncated` + `pr.Truncated` |
| `clients/python/events_client.py:523` | `if r.get("cursorGap"):` | `if r.get("truncated"):` |
| `README.md:80, 98, 125, 131, 270` | doc / table cells using `cursorGap` / `CursorGap` | spec name `truncated` / `Truncated` |

No SDK accessor renames in `clients/go/event.go` — the field doesn't surface there (it's poll-result-shape, not event-shape).

### 2. Drop `results: [{...}]` wrapper from `events/poll` response

After phase 1's batching removal, the wrapper always carries exactly one entry. Lift its fields to top level per the latest spec.

**Before** (`events.go:318-325`):
```jsonc
{ "results": [{ "id": "demo", "events": [...], "cursor": "...", "hasMore": false, "cursorGap": false, "nextPollSeconds": 5 }] }
```

**After**:
```jsonc
{ "events": [...], "cursor": "...", "hasMore": false, "truncated": false, "nextPollSeconds": 5 }
```

**Site changes:**
- `events.go:279-285` (EventNotFound path) — emit flat error response `{"error": {"code": -32011, "message": "EventNotFound"}}` directly, not wrapped in `results[]`. Actually, since this is a JSON-RPC error not a per-result error, it should be a real JSON-RPC error response (`core.NewErrorResponse(id, ErrCodeEventNotFound, "EventNotFound")`). Today it's wrapped per the old "partial success" model; the spec's flat shape makes it unambiguously a top-level error.
- `events.go:318-325` (success path) — emit flat success response per the example above.
- `events.go:225-236` (`pollResultWire` struct) — drop the `Error` field (unreachable after the EventNotFound path becomes a real error response). Keep the rest, rename `CursorGap` → `Truncated`.
- `clients/python/events_client.py:178, 519-523` (`results = resp.get("result", {}).get("results", [])`) — read top-level fields directly.

The `Error *struct{...}` embedded in `pollResultWire` becomes dead code once EventNotFound is hoisted to a JSON-RPC error. Removing it keeps the type honest.

### 3. Error code remap

New named constants in a new file `experimental/ext/events/errors.go` (mirrors the `core/jsonrpc.go` `ErrCode*` pattern). Only the codes α actually emits — γ/ζ add the others in the commits that use them, so reviewers get context for each constant alongside the behavior that needs it.

```go
// experimental/ext/events/errors.go
package events

// JSON-RPC error codes for the MCP Events extension. Drawn from the
// reserved range -32011..-32017 (-32014 is unused/reserved) so they
// don't collide with base MCP error codes.
const (
    ErrCodeEventNotFound      = -32011 // Unknown event name
    ErrCodeInvalidCallbackUrl = -32015 // Webhook URL unreachable / rejected
)
```

**Site swaps** (3 call sites today, 3 constants used):
- `events.go:284` `-32001` → `ErrCodeEventNotFound` (`-32011`)
- `events.go:346` `-32001` → `ErrCodeEventNotFound` (`-32011`)
- `events.go:355` `-32005` → `ErrCodeInvalidCallbackUrl` (`-32015`)

The `Code int` literal `-32001` in the `pollResultWire.Error` struct (events.go:284) becomes irrelevant if delta 2 above hoists EventNotFound to a JSON-RPC error response. So this swap is two-line if delta 2 lands; one-line otherwise. Keep both — the constant lives in the new file regardless.

### 4. `webhook-id` header on event deliveries = `eventId`

Standard Webhooks dedup hinges on `webhook-id` being **stable across retries of the same event**. Our current code (`headers.go:99` calls `newMessageID()` inside `signStandardWebhooks`, which is called inside the retry loop at `webhook.go:272`) generates a fresh `msg_<random>` per retry attempt — silently breaking receiver-side dedup.

The spec's fix: use the event's own `eventId` as `webhook-id`. Same eventId → same webhook-id → receiver dedup works. Side effect: this also closes the silent dedup bug.

**Sign functions take a message-ID parameter:**

```go
// before
func signStandardWebhooks(body []byte, secret string, now time.Time) signedDelivery
func signMCP(body []byte, secret string, now time.Time) signedDelivery
func signFor(mode WebhookHeaderMode, body []byte, secret string, now time.Time) signedDelivery

// after
func signStandardWebhooks(msgID string, body []byte, secret string, now time.Time) signedDelivery
func signMCP(msgID string, body []byte, secret string, now time.Time) signedDelivery  // reserved for control envelopes / future
func signFor(mode WebhookHeaderMode, msgID string, body []byte, secret string, now time.Time) signedDelivery
```

**Caller change** (`webhook.go:272`):
```go
// before
signed := signFor(r.headerMode, body, target.Secret, time.Now())

// after
signed := signFor(r.headerMode, event.EventID, body, target.Secret, time.Now())
```

`newMessageID()` stays in the codebase. ζ will use it for control envelopes (`msg_<type>_<random>`). Don't rename or move it in α — keeps the diff narrow.

**MCPHeaders mode** (legacy): `signMCP` doesn't currently emit a `webhook-id`. Spec doesn't mandate one for the legacy headers either. Plumbing the `msgID` parameter through and ignoring it inside `signMCP` is fine — keeps the function signature consistent across modes.

## Files to modify

| File | Lines changed (rough) |
|---|---|
| `experimental/ext/events/errors.go` (NEW) | ~15 |
| `experimental/ext/events/events.go` | ~15 (rename + flatten + error consts) |
| `experimental/ext/events/yield.go` | 1 line (rename in PollResult constructor) |
| `experimental/ext/events/headers.go` | 4 functions get a parameter; ~10 lines |
| `experimental/ext/events/webhook.go` | 1 line (pass `event.EventID` to signer) |
| `experimental/ext/events/yield_test.go` | rename test + accessor (~5 lines) |
| `experimental/ext/events/headers_test.go` | update assertions for the new webhook-id source (~10 lines) |
| `experimental/ext/events/clients/python/events_client.py` | 2 sites (poll-response decode + cursorGap field name) |
| `experimental/ext/events/README.md` | 5 sites (terminology updates) |
| (NEW) `experimental/ext/events/wire_shape_test.go` | golden-shape test for `events/poll` response (~50 lines) |

Total: ~110 lines changed across 10 files. ~15 added (the new errors.go + the wire-shape test); rest are renames / edits.

## Tests

### Red-before-green tests (must fail before, pass after)

1. **`TestPollResponse_FlatShape_Truncated`** (NEW in `wire_shape_test.go`) — make a poll request, decode the response, assert top-level keys: `events`, `cursor`, `hasMore`, `truncated`, `nextPollSeconds`. Assert NO `results` key. Doc comment: "Verifies the events/poll response uses the flat top-level shape per the spec, not the legacy results[] wrapper from the batching era. Failing this test means a client decoding the spec shape would not find its data."

2. **`TestPollResponse_EventNotFound_IsJSONRPCError`** (NEW in `wire_shape_test.go`) — poll for an unknown event name, assert the response is a JSON-RPC error with code `-32011` (`ErrCodeEventNotFound`), not a wrapped per-result error. Doc comment: "Verifies that EventNotFound surfaces as a top-level JSON-RPC error per the spec, not as an embedded result-level error from the legacy partial-success model. Aligns with spec error code -32011."

3. **`TestStandardWebhooks_WebhookIDIsEventID`** (extension to `headers_test.go`) — sign two retries of the same event, assert the `webhook-id` header is identical across both, and assert it equals the event's `EventID`. Doc comment: "Verifies the webhook-id header is sourced from eventId, not regenerated per retry. The Standard Webhooks dedup contract requires the receiver to dedup by webhook-id; if it changes per retry, dedup silently breaks. Spec mandates webhook-id = eventId for event deliveries."

4. **`TestEventNotFound_UsesSpecCode`** (extension to `events.go` test, or new) — invoke an `events/subscribe` with an unknown event name, assert response error code is `-32011`. Same for `events/poll`. And `events/subscribe` with a non-https url returns `-32015`. Doc comment: "Verifies our error codes are in the spec-mandated -32011..-32017 range. The previous -32001/-32005 codes collided semantically with base MCP error codes."

### Existing test updates (assertion-renames only, NOT relaxations)

- `yield_test.go:81-97`: `TestYieldingSource_EvictionAndCursorGap` → `TestYieldingSource_EvictionAndTruncated`; `pr.CursorGap` → `pr.Truncated`. Same assertion, renamed access.
- `headers_test.go` existing webhook-id presence test: keep the "must not be empty" assertion, ADD an "equals eventId" assertion. Strict-strengthening, not weakening.
- `events_test.go` (if any test today asserts `-32001`/`-32005`): swap to `-32011`/`-32015`. Same comparison, new constant.

**Assertion delta tracking** (per CLAUDE.md test-loosening discipline):
- Added: 4 new test functions, 4-6 new assertions
- Removed/weakened: 0 (renames don't count; constant swaps don't count)
- Net: strict-strengthening

### Integration test candidates (deferred — not in α)

- End-to-end discord demo `make demo` against a server running α — verify the wire bytes a real client sees match the new shape. Worth doing manually before opening the PR; not a code-test.
- TS reference SDK interop — they'll still emit the OLD wire shape until they migrate. Will need either parser tolerance or version negotiation. Out of scope for α; track in #349.

## Constraints check

| Constraint | Status |
|---|---|
| No CONSTRAINTS.md in this project | OK — none to validate against |
| No globals (CLAUDE.md global guidance) | OK — error codes are package constants, not mutable state |
| No new abstractions for hypothetical futures | OK — adding 4 unused error code constants but they're for the next 2 PRs in the plan, not speculative |
| No backwards-compat shims | OK — wire-format break is intentional; `experimental/` carries no stability promise |
| Single-delta-per-commit principle (this plan) | OK — 4 commits proposed below, one per delta |

## Pushdown / splitdown analysis

| Item | Project-specific? | Pushdown candidate? | Target | Rationale |
|---|---|---|---|---|
| `cursorGap` → `truncated` rename | Yes | No | n/a | Wire field is events-extension-specific |
| Drop `results[]` wrapper | Yes | No | n/a | Same |
| `webhook-id` = eventId | Yes | No | n/a | Webhook delivery is events-extension-specific |
| Error code constants | Could push to `core` | No (yet) | Stay in `experimental/ext/events/errors.go` | Codes are events-extension-specific (-32011..-32017 range is reserved for events). Don't pollute `core`. |

No splitdowns identified.

## Commits

Four atomic commits, one per delta. Each cite the spec section it implements.

1. `feat(events): add ErrCode* constants for spec error codes -32011..-32017`
   — new `errors.go` with the 6 constants. No call-site changes yet.

2. `feat(events): rename PollResult.CursorGap to .Truncated (events alignment α)`
   — Go-level rename in `events.go`, `yield.go`, `yield_test.go`. The wire field name change happens in the next commit.

3. `feat(events): flatten events/poll response, swap to spec error codes`
   — drop `results[]` wrapper, rename `cursorGap` JSON field to `truncated`, swap the 3 error code call sites to constants. Adds wire-shape tests. Updates Python SDK decoder.

4. `feat(events): use eventId as webhook-id for event deliveries`
   — sign-function signature changes, webhook.go caller change, headers test extension. Adds the dedup-stability test.

5. `docs(events): update README terminology for truncated/error codes`
   — README touch-ups. Single-purpose so the doc diff is reviewable separately.

(Five commits. Renumbered: docs commit at the end, separate from code so reviewers can split attention.)

## Acceptance criteria

- `cd experimental/ext/events && go test ./...` green
- `cd examples/events/discord && make test` green
- `cd examples/events/telegram && make test` green
- `make test-experimental` green at repo root
- `grep -rn "cursorGap\|CursorGap" experimental/ext/events/ examples/events/` returns zero matches outside this PR's diff
- `grep -rn "\\-3200[15]" experimental/ext/events/` returns zero matches
- `grep -rn "results.*\[\\]pollResultWire\|\"results\"" experimental/ext/events/` returns zero matches
- Wire-shape test passes against an actual `events/poll` round-trip
- Manual: run the discord demo `make demo` end to end, eyeball step 4 (poll) wire bytes for the flat shape

## Out of scope for α (explicitly)

- Renaming `newMessageID` to `newControlMessageID` — premature; ζ will rework anyway
- Adding `_meta` field to events / event-defs — gap #5 in the umbrella plan, future PR
- Adding `X-MCP-Subscription-Id` header — γ delivery
- Adding control envelope handling (`type:gap`, `type:terminated`) — ζ
- Adding `maxAge` parameter — δ
- Anything touching auth / tuple identity — γ

If any of the above creeps in during implementation, push it back into its proper PR group.

## Risk

**Low.** The change is mechanical: rename, flatten, swap. The only non-mechanical insight is the eventId-as-webhook-id discovery (which is a silent bug fix, not a regression). All renames are strict-strengthening on the test side.

Mitigations:
- Each commit is independently revertable (atomic per delta)
- The PR doesn't change behavior — the demo at HEAD should look identical to a user (same events, same delivery), only the wire bytes differ
- A curious reviewer can decode the wire trace from a `make demo` run and compare against the spec sketch's example responses byte-for-byte

## Done when

- All 5 commits land
- PR #XXX merged into main
- #349 updated with "α merged ✅" + commit hash
- This PLAN.md replaced (next group's PLAN, or removed if no next group is in flight)
