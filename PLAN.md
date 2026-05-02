# Plan: Events spec alignment β — collapse webhook secret modes

**Issue:** mcpkit 349 (umbrella) | **Group:** β (second of 7)
**Branch:** `feat/events-beta-secret-collapse` (from main)
**Depends on:** α (merged via #353)
**Unblocks:** γ (auth + tuple identity reuses the secret model β leaves behind)

## Summary

Spec collapsed the webhook secret model from three modes (`Server` / `Client` / `Identity`) to one: client-supplied REQUIRED, `whsec_` + base64 of 24-64 random bytes, validated, never echoed back in subscribe response. This PR deletes everything but the client-supplied path. Net-deletion PR — ~150 LOC out, ~30 LOC in (validator + adjusted subscribe response).

The spec didn't move because of any bug we filed; it moved because the security property "HMAC proves the endpoint owner asked for this delivery" requires the endpoint (which is also the verifier) to choose the secret. With server-generated, HMAC only proves "this came from the MCP server" — leaving open a third-party-target abuse where anyone could subscribe with `url=<victim>`. Background: spec sketch line 349, 474, and the *Webhook secret generation* decision in the Key Design Decisions table.

Side effect: the rotation-induced sig-FAIL bug we hit during α validation (server's `resolveSecret` regenerated a new secret on every refresh, client-side python never re-adopted it) becomes structurally impossible. Server has no choice but to use what the client supplied; client supplies the same value on every refresh; no rotation surprise.

## Spec deltas addressed

1. **`delivery.secret` is REQUIRED** on `events/subscribe`. Servers MUST reject missing or malformed values with `-32602 InvalidParams`.
2. **Format MUST be `whsec_` + base64 of 24-64 random bytes.** Not just any string. Validator runs at the handler boundary.
3. **Subscribe response no longer carries `secret`** — server doesn't generate one, so there's nothing to echo back.
4. **`events/unsubscribe` resolves by tuple, not by secret.** The "proof-of-possession via constant-time secret compare" path (`UnregisterBySecret`) goes away. (Tuple identity is γ; this PR drops the secret-form unsubscribe but leaves the `id`-form intact for now — γ will replace `id` with `(name, params, url)`.)

## Four scope buckets

### Bucket A — DELETE (the bulk of the work)

| File | What goes |
|---|---|
| `experimental/ext/events/secret.go` | DELETE entirely. The whole file is mode-related: `WebhookSecretMode` enum, `WebhookSecretServer` / `Client` / `Identity` constants, `String()` method, `ParseSecretMode`, `canonicalTuple`, `deriveIdentitySecret`, `deriveIdentityID`. ~128 lines gone. The `generateSecret` function moves to a new home (see Bucket C). |
| `experimental/ext/events/registry_modes_test.go` | DELETE entirely. ~169 lines, all mode-coverage tests. |
| `experimental/ext/events/secret_test.go` | Trim heavily. Drop `TestParseSecretMode_Aliases`, `TestParseSecretMode_Unknown`, `TestCanonicalTuple_*`, `TestDeriveIdentitySecret_*`, `TestDeriveIdentityID_*`. Keep only what tests the secret-format validator (new in Bucket C) and `generateSecret`'s `whsec_` prefix. ~80 lines gone, ~25 lines reshaped. |
| `experimental/ext/events/webhook.go` | Delete `WithWebhookSecretMode`, `WithWebhookRoot`, `secretMode` field, `root` field, `resolveSecret`, `SecretMode()`, `UnregisterBySecret`. ~50 lines gone. The Identity-mode panic check at construction goes too. |
| `experimental/ext/events/events.go` | `events/subscribe` handler: drop the `resolveSecret` call. Drop the `Params` field from the request (Identity-mode tuple input). `events/unsubscribe` handler: drop the `req.Delivery.Secret` proof-of-possession path; require `id` (γ will swap to tuple). ~25 lines net change. |
| `experimental/ext/events/clients/go/eventsclient_test.go` | Drop the Identity-mode interop test. ~30 lines gone. |
| `examples/events/discord/main.go` | Drop `--webhook-secret-mode` and `--webhook-root` flags. ~20 lines gone. |
| `examples/events/telegram/main.go` | Same. ~20 lines gone. |
| `examples/events/discord/e2e_test.go` | Drop the Identity-mode test (lines 276-277 reference). ~30 lines gone. |
| `examples/events/discord/README.md` + `experimental/ext/events/README.md` + `experimental/ext/events/DEPLOYMENT.md` | Drop the secret-mode comparison sections, master-root handling guidance, mode flag examples. |

### Bucket B — VALIDATE (new behavior in the subscribe handler)

The spec mandates strict format validation. New helper in the events package:

```go
// experimental/ext/events/secret.go (slimmed file, replaces deleted version)
package events

import (
    "crypto/rand"
    "encoding/base64"
    "errors"
    "strings"
)

const webhookSecretPrefix = "whsec_"

// generateSecret returns a Standard-Webhooks-conformant client-side secret.
// Client SDKs use this to auto-generate when the application doesn't supply
// one. The value is whsec_ + base64 of 32 random bytes (within the spec's
// 24-64 byte range).
func generateSecret() string { ... existing impl ... }

// validateClientSecret enforces the spec format: whsec_ followed by a
// base64-encoded value that decodes to 24-64 bytes. Returns nil on
// success or a descriptive error suitable for use as the message of a
// -32602 InvalidParams response.
func validateClientSecret(s string) error { ... }
```

The handler:

```go
// events/subscribe handler — relevant fragment after β
if req.Delivery.Secret == "" {
    return core.NewErrorResponse(id, core.ErrCodeInvalidParams,
        "delivery.secret is required (must be whsec_<base64 of 24-64 random bytes>)")
}
if err := validateClientSecret(req.Delivery.Secret); err != nil {
    return core.NewErrorResponse(id, core.ErrCodeInvalidParams,
        "delivery.secret invalid: "+err.Error())
}
// store as-is in the registry — no resolveSecret call
expiresAt := webhooks.Register(req.ID, req.Delivery.URL, req.Delivery.Secret)
```

### Bucket C — RESHAPE (response shape + Go SDK)

`events/subscribe` response no longer carries `secret`:

```jsonc
// before
{ "id": "...", "secret": "whsec_...", "cursor": "...", "refreshBefore": "..." }

// after
{ "id": "...", "cursor": "...", "refreshBefore": "..." }
```

Go SDK changes (`experimental/ext/events/clients/go/subscription.go`):

- `SubscribeOptions.Secret` documentation updated: client-supplied is the only path; SDK auto-generates `whsec_` value if `Secret` is empty.
- `subscribe()` always supplies a non-empty secret (auto-generates if `opts.Secret == ""`).
- The response decoder drops the `Secret` field — server doesn't return it.
- `Subscription.Secret()` returns the value the SDK supplied (or auto-generated), not what the server returned.

Python SDK changes (`experimental/ext/events/clients/python/events_client.py`):

- `WebhookSubscription.__init__` requires `secret` to be a valid `whsec_` value or empty (auto-generate). Validates client-side before sending.
- `_subscribe()` doesn't read `result.get("secret")` — server doesn't return it. Stores the supplied value as `self.secret`.
- `cmd_webhook` (the `webhook` subcommand): the `secret_holder` indirection becomes unnecessary since the value never changes mid-flight. Simplifies to a stable `secret` variable.
- `--secret` CLI flag default: change from `"demo-webhook-secret"` (invalid format!) to auto-generate via `generate_webhook_secret()` helper.

### Bucket D — DOCS

- `experimental/ext/events/README.md`: drop the "Secret mode" section. Add a one-paragraph "Webhook secret" section: client supplies; spec format; SDKs auto-generate.
- `experimental/ext/events/DEPLOYMENT.md`: drop the "master root credential" guidance section. Add a sentence noting the spec requires client-supplied secrets and what that means for proxies that need to share secret state across receivers.
- `examples/events/discord/README.md`: drop the `--webhook-secret-mode` flag examples.

## Files NOT touched (intentionally)

- `experimental/ext/events/headers.go` — α already moved this onto Standard Webhooks signing. β doesn't change signing.
- `experimental/ext/events/webhook.go` Register / Unregister / pruneExpiredLocked — these still take a secret; β just changes the *source* of the secret (always from the client now). The signature on Register doesn't change.
- `core/jsonrpc.go` — base error codes (`ErrCodeInvalidParams`) are already in core; no changes needed.
- `experimental/ext/events/clients/go/receiver.go` — verifies inbound signatures; secret model is irrelevant to the receiver.

## Tests

### Red-before-green tests

1. **`TestSubscribe_RejectsMissingSecret`** (NEW in `wire_shape_test.go` or a new `secret_validation_test.go`) — invoke subscribe handler with `delivery.secret = ""`, assert response is `-32602 InvalidParams`. Doc comment: "Per the spec, delivery.secret is REQUIRED on events/subscribe; the server MUST reject missing values. Failing this test means we accept subscribes with no secret, exposing receivers to deliveries they can't verify."
2. **`TestSubscribe_RejectsMalformedSecret`** (NEW) — try several bad inputs: missing prefix, prefix only, base64 of 23 bytes (under 24), base64 of 65 bytes (over 64), non-base64 garbage. Assert each returns `-32602`. Doc comment: "Spec mandates whsec_ + base64 of 24-64 random bytes. Receivers will reject deliveries signed with weak / wrong-format secrets — better to fail at subscribe time than silently issue a subscription that can never deliver."
3. **`TestSubscribe_AcceptsValidWhsecSecret`** (NEW) — happy path: subscribe with a valid `whsec_<32-byte-b64>` value, assert success and that response does NOT contain a `secret` field. Doc comment: "Spec response shape no longer echoes the secret; client owns it. Failing this test means we leak the supplied secret back through the response or fail to accept a valid one."
4. **`TestUnsubscribe_RejectsSecretForm`** (NEW) — invoke unsubscribe with `delivery.secret` but no `id`, assert error. Doc comment: "The proof-of-possession unsubscribe path is gone; only the id-keyed form is accepted. γ will replace id with the tuple form."

### Existing test updates

- `secret_test.go`: trimmed to the validator + generateSecret tests. ~25 lines remain; assertion strength preserved on what stays.
- `headers_test.go`: no changes needed (α handled it).
- `wire_shape_test.go`: existing tests still pass (response struct shape changes are additive — drop secret field; existing assertions don't rely on it).
- Demo `e2e_test.go` files: drop the Identity-mode test in discord; rest unchanged.

### Assertion delta tracking

- Added: 4 new test functions, 8-12 new assertions
- Removed: ~5 test functions deleted (Identity-mode, parse-mode, canonical tuple, derive helpers) — these test code that no longer exists, deletion is appropriate
- Weakened: 0 — no live assertions are loosened, only deletion of tests for deleted code

## Constraints check

| Constraint | Status |
|---|---|
| No CONSTRAINTS.md | OK |
| No globals | OK — `webhookSecretPrefix` is a package constant (not state) |
| No new abstractions for hypothetical futures | OK — validator is for what α/β/γ/δ all need (spec-mandated) |
| No backwards-compat shims | OK — wire format break is intentional; experimental/ carries no stability promise. We delete cleanly, don't deprecate. |
| Deprecate-then-delete for spec-tracking removals | **Override**: spec actively simplified (three modes → one); no plausible reversal exists. The plan-doc explicitly calls this out as a "delete" not "deprecate" case. Same logic α used for the wire renames. |
| Single-delta-per-commit principle | Applied — see commit ordering below |

## Commits

Five commits. The first two are pure deletion; the third introduces the validator; the fourth wires it into the handler and reshapes the response; the fifth updates clients + docs.

1. `feat(events): delete WebhookSecretMode (Server / Client / Identity), drop Identity crypto`
   — Pure deletion: `secret.go` enum + helpers, `webhook.go` mode-related options + resolveSecret + UnregisterBySecret + secretMode field, `registry_modes_test.go` (whole file), most of `secret_test.go`. Compile breaks deliberately — fixed in next commit.
   — Note: this commit does NOT compile on its own (callers in events.go still reference the deleted symbols). That's intentional — the next commit fixes them. We could split-and-stub but the diff is cleaner as one logical "remove the modes" then "wire the new behavior" pair.
   — *Reconsider during impl*: if reviewer sentiment prefers always-compiles-per-commit, fold commits 1+2 together.

2. `feat(events): subscribe handler uses client-supplied secret directly; drop secret-form unsubscribe`
   — Updates `events.go` to drop the resolveSecret call and the secret-form unsubscribe. Drops the request-body `Params` field (Identity-mode artifact). At this point the build is green again.
   — Adds the validator helper (`validateClientSecret`) and wires it. Adds the four red-before-green tests in a new `secret_validation_test.go`.

3. `feat(events): drop secret from subscribe response (server no longer generates)`
   — `events.go` subscribe response: omit `secret`. Adjust the docstring on `pollResultWire`-adjacent docs as needed.
   — Updates Go SDK `subscription.go`: `subscribe()` decoder drops `Secret` field; `subscribe()` always sends a non-empty secret (auto-generates if user didn't supply).
   — Updates Python SDK: same shape change in `_subscribe()`; `secret_holder` indirection collapses to a plain variable.

4. `feat(events): demo + client-side cleanup for client-supplied-only secrets`
   — Drop `--webhook-secret-mode` / `--webhook-root` flags from discord + telegram main.go. Drop the Identity-mode test in `examples/events/discord/e2e_test.go`. Drop the Server/Identity readings from python `cmd_webhook`.
   — `--secret` CLI flag default changes from `"demo-webhook-secret"` (invalid format) to auto-generate.

5. `docs(events): update README + DEPLOYMENT for client-supplied-only secret model`
   — `experimental/ext/events/README.md` Secret mode section → one-paragraph "Webhook secret" section.
   — `experimental/ext/events/DEPLOYMENT.md` master-root section deleted.
   — `examples/events/discord/README.md` flag examples deleted.
   — Single-purpose docs commit so the doc diff is reviewable separately.

## Acceptance criteria

- `cd experimental/ext/events && go test ./...` green
- `cd examples/events/discord && go test ./...` green
- `cd examples/events/telegram && go test ./...` green
- `grep -rn "WebhookSecretMode\|WebhookSecretServer\|WebhookSecretClient\|WebhookSecretIdentity\|WithWebhookSecretMode\|WithWebhookRoot\|deriveIdentity\|canonicalTuple\|UnregisterBySecret\|resolveSecret\|ParseSecretMode\|webhook-secret-mode\|webhook-root" experimental/ext/events/ examples/events/` returns zero matches
- Subscribe with no `delivery.secret` returns `-32602`
- Subscribe with `delivery.secret = "wrong"` returns `-32602`
- Subscribe with valid `whsec_<base64-of-32-bytes>` succeeds and response does NOT contain `secret`
- Manual: run discord demo `make demo` against `make serve` (no special flags) — verify the rotation sig-FAIL we caught during α is gone (deliveries should ALL show "sig OK" indefinitely)

## Out of scope for β (explicitly)

- **Auth + tuple subscription identity** — γ. The `id` input on subscribe and unsubscribe stays as-is for β; γ replaces it with `(principal, name, params, url)`.
- **Standard Webhooks multi-signature dual-signing during rotation grace** — spec mentions this for client-initiated secret rotation. Implementing requires the registry to hold old + new secret simultaneously for a grace window. Defer; not load-bearing for our demos.
- **`maxAge` / flat `events/poll` request shape** — δ.
- **Push delivery** — ε.

## Risk

**Low.** Deletion is mechanical. The validator is small and well-specified by the spec. The main blast radius is "anyone calling our SDK with empty secret will start getting auto-generated `whsec_` values" — which is correct behavior, not a regression. `experimental/` carries no stability promise so even external consumers (none we know of) get fair warning.

The commit-1-doesn't-compile choice is the only mild concern. Mitigation: rebase-squash 1+2 into a single commit if the reviewer prefers always-compiles-per-commit. The tradeoff is "narrower commit messages" vs "always green at every commit". I'll start with separate commits and squash on review feedback if asked.

## Done when

- All 5 commits land
- PR #XXX merged into main
- #349 updated with "β merged ✅" + commit hash
- This PLAN.md retired (deleted; γ writes its own when its branch opens)
- Manually verify the discord demo runs cleanly with `make serve` + `make webhook` end-to-end (no sig FAIL) — closes out the rotation bug we caught during α validation
