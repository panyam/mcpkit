# Plan: Events spec alignment γ — auth + tuple subscription identity

**Issue:** mcpkit 349 (umbrella) | **Group:** γ (third of 7)
**Branch:** `feat/events-gamma-auth-tuple` (from main, post-β-merge)
**Depends on:** α (#353 merged dc0f8aa) and β (#355 merged 96b41d1)
**Unblocks:** ζ (control envelopes use the derived id from γ; `X-MCP-Subscription-Id` header lives in ζ but γ provides the value)

**Spec snapshot:** [`proposals/triggers-events-wg/spec-snapshots/design-sketch-2026-04-30.md`](https://github.com/panyam/mcpcontribs/blob/main/proposals/triggers-events-wg/spec-snapshots/design-sketch-2026-04-30.md). Citations below in the form `(§"Section" L<line>)`.

## Summary

Move webhook subscription identity onto the spec's `(principal, delivery.url, name, params)` tuple, drop the client-supplied `id`, derive a deterministic server-side `id` for receiver routing, gate webhook subscribe/unsubscribe on auth (with a demo-only escape hatch), and emit the routing handle as `X-MCP-Subscription-Id` on every delivery POST.

This is the largest behavioral change in the alignment plan. Mitigations: hybrid auth (auto-detects real OAuth/Keycloak via env var, falls back to a demo principal otherwise), feature-gated rollout per commit, atomic commits each green at HEAD.

## Spec deltas addressed

| # | Delta | Spec citation |
|---|---|---|
| 1 | `events/subscribe` and `events/unsubscribe` MUST require an authenticated principal; reject unauth with `-32012 Unauthorized` | §"Subscription Identity" → "Authentication required" L361; error code table L110 |
| 2 | Subscription key is `(principal, delivery.url, name, params)`, `params` compared by canonical-JSON equality, all four immutable for the sub's lifetime | §"Subscription Identity" → "Key composition" L363 |
| 3 | No client-generated `id`. Server derives a deterministic SHA-256 over the canonical key serialization | §"Subscription Identity" → "Derived `id`" L367 |
| 4 | The derived `id` is a routing handle, not a capability — knowing it does not authorize anything | §"Subscription Identity" → "Derived `id`" L367 + "Cross-tenant isolation" L378 |
| 5 | `events/unsubscribe` resolves by tuple — `(name, params, delivery.url)` from the request, `principal` from auth context, derived `id` not accepted as input | §"Unsubscribing: events/unsubscribe" L509 |
| 6 | `X-MCP-Subscription-Id` header on every webhook delivery POST | §"Webhook Event Delivery" L390 + §"Webhook Security" → "Signature scheme" L472 |

## Hybrid auth design (the demo escape hatch)

The spec mandates "MUST reject unauthenticated calls with `-32012`" (L361). Our discord/telegram demos run anonymous today (no `WithAuth(...)` on the server). Enforcing the spec strictly would require every demo to stand up an OAuth provider just to run `make demo`.

**Solution: detect at runtime.** If real auth is wired, follow spec strictly. If not, fall back to a demo-only "anonymous principal" knob with explicit `Unsafe` naming and startup warning logs.

```go
// New field on events.Config
type Config struct {
    Sources                  []EventSource
    Webhooks                 *WebhookRegistry
    Server                   *server.Server
    UnsafeAnonymousPrincipal string  // demo-only; empty = strict spec
}
```

**Handler logic** (in `registerSubscribe` and `registerUnsubscribe`):

```go
principal := ""
if claims := ctx.AuthClaims(); claims != nil {
    principal = claims.Subject                 // path 1: real auth (spec-correct)
} else if cfg.UnsafeAnonymousPrincipal != "" {
    principal = cfg.UnsafeAnonymousPrincipal   // path 2: demo escape hatch
} else {
    return core.NewErrorResponse(id, ErrCodeUnauthorized, "Unauthorized")  // path 3: spec strict
}
```

**Demo wiring** (`examples/events/discord/main.go`, telegram parallel):

```go
cfg := events.Config{Sources: ..., Webhooks: webhooks, Server: srv}
if issuer := os.Getenv("OAUTH_ISSUER"); issuer != "" {
    // KCL / any OIDC provider detected — wire real auth
    srv.UseAuth(buildValidator(issuer))
    log.Printf("[server] auth: real OIDC provider at %s — webhook subscribe requires authenticated principal", issuer)
} else {
    cfg.UnsafeAnonymousPrincipal = "demo-user"
    log.Printf("[server] auth: demo mode — webhook subscribe accepts anonymous (DEVIATES from spec). Set OAUTH_ISSUER to enable real auth.")
}
events.Register(cfg)
```

**What this buys**:
- `make demo` works out of the box (anonymous, demo principal stands in for `claims.Subject`)
- `make kcl && OAUTH_ISSUER=http://localhost:8081/realms/demo make demo` runs against Keycloak with real auth, full spec behavior
- Production deployments that wire `WithAuth(...)` and don't set `UnsafeAnonymousPrincipal` get spec-mandated `-32012` for anonymous subscribe attempts
- Misconfigured deployments (no auth, no anonymous fallback) also get `-32012` — fail-loud is good

**Deviation cost is contained**: only one named knob (`UnsafeAnonymousPrincipal`), `Unsafe` prefix, startup log warning, defaults to off. Tests can set it to keep working without standing up auth.

## Commits

5 atomic commits. Each builds and tests green at HEAD.

### γ-1 — tuple identity helpers (no behavior change)

Add `canonicalKey(principal, url, name, params) []byte` and `deriveSubscriptionID(canonicalKey) string` as pure-functional helpers in a new file `experimental/ext/events/identity.go`. Unit tests cover canonical-JSON equality of params, sort-stability, principal isolation, derivation determinism, and id collision-resistance for typical inputs.

No callers yet — registry + handlers continue using `(url, id)` keys. Lets γ-2 reach for proven helpers.

**Files**: `experimental/ext/events/identity.go` (NEW, ~60 LOC), `experimental/ext/events/identity_test.go` (NEW, ~80 LOC).

**Spec cite for code comments**: `(§"Subscription Identity" → "Key composition" L363, "Derived `id`" L367)`

### γ-2 — auth gate + tuple-keyed registry + UnsafeAnonymousPrincipal escape

The behavioral change. Subscribe handler reads principal via the path-1/2/3 logic; registry rekeys on the tuple via canonicalKey from γ-1; deriveSubscriptionID becomes the routing id.

**Files**:
- `experimental/ext/events/events.go` — Subscribe handler reads principal, derives id from canonicalKey(principal, url, name, params), passes to Register. Drops the `id` request field. Same for Unsubscribe. Returns `-32012 Unauthorized` on path-3.
- `experimental/ext/events/webhook.go` — `Register(canonicalKey []byte, derivedID, url, secret string)`. Internal `targets` map keyed on canonicalKey hex (or string-encoded bytes) instead of `(url, id)`. `Unregister(canonicalKey []byte)`.
- `experimental/ext/events/errors.go` — Add `ErrCodeUnauthorized = -32012` (we left this out of α; γ needs it).
- `experimental/ext/events/secret_validation_test.go` — Tests already use `srv.Dispatch` with the dispatcher in init state. Add `UnsafeAnonymousPrincipal: "test"` to the Register call in `buildSecretValidationStack` so existing tests keep working. Add NEW tests for the strict-spec path-3 rejection.

**Tests** (red-before-green):
- `TestSubscribe_RejectsAnonymousWhenStrict` — Register with no `UnsafeAnonymousPrincipal`, no auth wired → subscribe returns `-32012 Unauthorized`.
- `TestSubscribe_AcceptsAnonymousWithEscape` — Register with `UnsafeAnonymousPrincipal: "demo"` → subscribe succeeds; the stored target's principal is "demo".
- `TestSubscribe_AcceptsRealAuth` — Register with no escape, but supply claims via test plumbing → subscribe succeeds; stored target's principal is `claims.Subject`.
- `TestSubscribe_TupleIsolation` — Two subscribes from different principals with same `(url, name, params)` → two distinct registry entries (cross-tenant isolation per L378).
- `TestSubscribe_TupleIdempotence` — Two subscribes from same principal with same `(url, name, params)` → one registry entry; second call refreshes TTL.
- `TestUnsubscribe_RejectsIDInput` (already exists in β; verify it still rejects).
- `TestUnsubscribe_ByTuple` — Unsubscribe with `(name, params, delivery.url)` removes the registered subscription.

**Spec cites for code comments**:
- Auth gate: `(§"Subscription Identity" → "Authentication required" L361)`
- Tuple key: `(§"Subscription Identity" → "Key composition" L363)`
- Cross-tenant isolation property: `(§"Subscription Identity" → "Cross-tenant isolation" L378)`

### γ-3 — drop client-supplied `id` from the wire; return server-derived only

Subscribe request no longer accepts `id`. Subscribe response continues to return `id` (the server-derived value). Unsubscribe request stops accepting `id` — only the tuple form per γ-2.

This is a wire-shape change separate from γ-2's auth+tuple logic so the diff stays narrow per delta.

**Files**:
- `experimental/ext/events/events.go` — Subscribe handler request struct loses the `ID string` field. Drops the line that uses it.
- `experimental/ext/events/clients/go/subscription.go` — `SubscribeOptions.SubID` field deleted. `subscribe()` no longer sends `id` in the request body. `Subscription.ID()` returns the server-derived value (this was already true post-β; just remove the SDK-side `SubID` plumbing).
- Python SDK — same shape change: `WebhookSubscription.__init__` loses `sub_id` kwarg.
- Discord + telegram demos — drop the `SubID:` arg in their `eventsclient.Subscribe(...)` calls.

**Tests**:
- `TestSubscribe_RejectsClientSuppliedID` (NEW) — request with `id` field returns `-32602 InvalidParams: client-supplied id is not accepted; server derives id over (principal, name, params, url)`.
- Update existing tests that pass `SubID` to drop the field.

**Spec cites**:
- `(§"Subscription Identity" → "Key composition" L363: "There is no client-generated id ... fully determined by what it listens for, where it delivers, and who asked")`

### γ-4 — `X-MCP-Subscription-Id` header on every webhook delivery

The header lives in delivery (a ζ-flavored concern), but γ produces the id value, so wiring the header here keeps the change atomic with the change that introduces the value. The webhook delivery code in `webhook.go::deliver` and the headers package both grow this.

**Files**:
- `experimental/ext/events/headers.go` — `signedDelivery` carries the subscription id; `applyHeaders` adds `X-MCP-Subscription-Id: <id>`.
- `experimental/ext/events/webhook.go` — `deliver(target, eventID, body)` becomes `deliver(target, eventID, subID, body)`. `Deliver(event)` looks up the target's derived id (from γ-2's tuple keying) and threads it through.
- `experimental/ext/events/clients/go/receiver.go` — already reads webhook headers via `r.Header.Get(...)`; no change needed for receiving the new header (consuming it lands in ζ along with `match`/`transform` per-sub routing). For γ, just emit the header.

**Tests**:
- `TestStandardWebhooks_EmitsSubscriptionIDHeader` (NEW) — make a delivery, assert `X-MCP-Subscription-Id` header is present and equals the subscription's derived id.
- `TestStandardWebhooks_SubscriptionIDStableAcrossRetries` (NEW) — pair with the existing `TestStandardWebhooks_WebhookIDIsStableAcrossRetries`. The subscription id MUST also be stable across retries (same sub, same id).

**Spec cites**:
- `(§"Webhook Event Delivery" L390: "X-MCP-Subscription-Id is an MCP-specific header... carrying the subscription id so the receiver can select the correct secret before parsing the body")`
- `(§"Webhook Security" → "Signature scheme" L472)`

### γ-5 — demo wiring + WALKTHROUGH regen + docs

Demos auto-detect: if `OAUTH_ISSUER` env var is set, wire real auth via `server.WithAuth(...)`. Otherwise set `events.Config.UnsafeAnonymousPrincipal: "demo-user"` and log a warning at server start.

Discord walkthrough's webhook step + new spec-validation steps (added in β-4-followup-2) work in both modes. Add a brand-new walkthrough step demonstrating the cross-tenant isolation property (two subscribes with different principals → distinct subscriptions).

**Files**:
- `examples/events/discord/main.go` — env-var detection, `WithAuth(...)` wiring or `UnsafeAnonymousPrincipal` fallback. ~30 LOC of demo code.
- `examples/events/telegram/main.go` — same pattern.
- `examples/events/discord/walkthrough.go` — NEW step demonstrating tuple identity / cross-tenant isolation if running in real-auth mode; gracefully no-ops in anonymous demo mode with a one-line note.
- `examples/events/discord/WALKTHROUGH.md` — regenerated via `make readme`.
- `examples/events/discord/README.md` + `telegram/README.md` — document the env-var detection (`OAUTH_ISSUER` to enable real auth; otherwise demo mode).
- `experimental/ext/events/README.md` — document the `UnsafeAnonymousPrincipal` option and its `Unsafe` prefix rationale.
- `experimental/ext/events/DEPLOYMENT.md` — note that production deployments should never set `UnsafeAnonymousPrincipal`; auth misconfiguration is detectable via the startup log.

**Tests**: end-to-end tests (`examples/events/discord/e2e_test.go`) need an init-time config decision. Tests can either:
1. Default to `UnsafeAnonymousPrincipal: "test"` (matches existing test behavior pre-γ).
2. Have a small subset that wires fake claims to exercise real-auth path-1.

I lean (1) for the bulk + (2) for ~3 new tests covering the auth-strict + auth-success paths. Existing assertions stay valid.

## Auth integration plumbing

The investigation in [task #60] mapped the surface:

| What | Where |
|---|---|
| Handler reads claims | `core.MethodContext.AuthClaims() *core.Claims` (`core/handler_context.go:183`) |
| Claims struct | `core/auth.go:8-25` — `Claims{Subject, Issuer, Audience, Scopes, Extra}` |
| Server wires auth | `server.WithAuth(validator)` (`examples/auth/main.go:302-332`) |
| When no auth: `claims == nil` | `server/server.go:1016-1027` `CheckAuth` returns `(nil, nil)` |
| Test plumbing for fake claims | `core.ContextWithSession(ctx, ..., &core.Claims{Subject: "test"})` (`ext/auth/scope_middleware_test.go:28-32`) |

For real-auth demo path, `OAUTH_ISSUER` env var triggers `examples/auth/common/setup.go::NewValidator(issuer)` (or equivalent) — same code path the existing auth examples use.

## Test impact summary

| Bucket | Count | Notes |
|---|---|---|
| New red-before-green tests | ~10 | Auth gate (3), tuple identity (3), webhook header (2), client-id rejection (1), tuple-form unsubscribe (1) |
| Existing tests updated | ~5 | secret_validation_test.go: add `UnsafeAnonymousPrincipal: "test"` to fixture. Demo e2e tests: same. |
| Tests deleted | 0 | γ adds capability, doesn't remove any |
| Strict-strengthening on assertions | ✅ | No weakened checks |

## Acceptance criteria

- `cd experimental/ext/events && go test -count=1 ./...` green
- `cd examples/events/discord && go test -count=1 ./...` green (demo defaults work)
- `cd examples/events/telegram && go test -count=1 ./...` green
- `grep -rn "SubID\|sub_id" experimental/ext/events/` returns 0 matches outside test fixtures (`SubID` field deleted from Go SDK)
- Manual: `make serve` (anonymous) + `make demo` works end-to-end including the webhook step (uses anonymous escape)
- Manual (if Keycloak available): `make kcl && OAUTH_ISSUER=... make serve` + `make demo` works end-to-end with real auth
- Wire bytes for an anonymous subscribe with no escape configured: `{"error":{"code":-32012,"message":"Unauthorized"}}`
- Wire bytes for any successful webhook delivery contain `X-MCP-Subscription-Id: <derived id>` header

## Out of scope for γ (explicitly)

- **Push delivery (`events/stream`)** — ε. Push has its own per-stream identity via `requestId`; γ's tuple identity is webhook-only.
- **Control envelopes (`type:gap`, `type:terminated`)** — ζ. They will use the `id` value γ produces but the envelope-emission code lives in ζ.
- **`deliveryStatus` on refresh response** — ζ.
- **`maxAge`** — δ.
- **Asymmetric server signing (ed25519, JWKS)** — Open Question 6 in the spec; not in any planned PR group yet.

## Risk

**Medium**. Touches the webhook critical path:
- registry rekey (γ-2) is the most invasive single change
- new fail-loud rejection path (`-32012` for anon when no escape) — could surprise an existing deployment that relies on anonymous webhook subscribes
- `OAUTH_ISSUER` auto-detect in demos changes behavior under env-var presence — needs to be visible in the startup log so reviewers know which mode is active

**Mitigations**:
- Each commit independently revertable
- The `Unsafe` prefix on the escape hatch + startup warning log surface the deviation
- Test coverage explicitly exercises path-1/2/3 transitions
- Demo defaults stay green without OAUTH_ISSUER set (we don't break `make demo`)

## Done when

- All 5 commits land
- PR opened linking #349
- PR merged
- #349 updated with γ merged ✅
- Root `PLAN.md` retired (δ writes its own)
- Manual verify: discord demo `make demo` works; if Keycloak is set up locally, the Keycloak flow works too

## Spec citation in commits

Every commit message body carries at least one spec citation in the form `(§"Section name" L<line>)` so the commit log itself is correlatable to the spec snapshot. Pattern was established in the umbrella plan citation pass (commit `844522a` on this branch).
