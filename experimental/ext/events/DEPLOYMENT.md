# Deployment Notes — `experimental/ext/events`

Operational guidance for running an MCP Events server with webhook delivery in private-cloud environments. Focused on the questions that come up when production WAFs and firewall teams get involved.

This is a living document — the events extension itself is experimental, and so are some of the deployment patterns. Where mcpkit ships partial implementation (e.g., `ValidateWebhookURL`'s loopback handling), this doc calls out what to add for production.

## Direction of traffic

Webhook delivery is **server → client**. The MCP server (your `events.WebhookRegistry`) is the HTTP *client*; the receiver URL is the HTTP *server*.

```
                                                   private cloud
                                                  ┌────────────────────┐
                                                  │                    │
  ┌──────────────┐         (1) events/subscribe   │  ┌──────────────┐  │
  │              │  ───────────────────────────►  │  │  Receiver    │  │
  │  MCP Host    │                                │  │  (HTTPS in)  │  │
  │  (host app)  │  ◄───────────────────────────  │  │              │  │
  │              │     refreshBefore, secret      │  └──────▲───────┘  │
  └──────┬───────┘                                │         │ POST     │
         │                                        │         │ + HMAC   │
         │ runs on / behind                       │         │          │
         ▼                                        │  ┌──────┴───────┐  │
  ┌──────────────┐         (2) POST events        │  │     WAF      │  │
  │              │  ───────────────────────────►  │  │  (allowlist) │  │
  │  MCP Server  │   X-MCP-Signature, ...         │  └──────▲───────┘  │
  │  + events    │                                │         │          │
  │  WebhookReg  │                                │         │ HTTPS    │
  │              │                                │         │          │
  └──────────────┘                                │         │          │
                                                  └─────────┼──────────┘
        ▲                                                   │
        │ public internet                                   │
        └───────────────────────────────────────────────────┘
```

Implications for firewalls:

- The MCP server needs **outbound** internet egress to reach receiver URLs. No inbound webhook traffic to the MCP server itself.
- The receiver lives **inside the customer's network**, behind their WAF. The WAF must allow the MCP server's source IPs to reach it.
- Subscribers (browsers, agents, etc.) connect to the MCP server over MCP's normal Streamable HTTP transport — that's a separate flow.

## What to put in your WAF

For deployments where the receiver sits behind a WAF, the rules need to allow webhook deliveries through. Recommended allowlist:

| Field | Value | Why |
|---|---|---|
| **Method** | `POST` | All deliveries are POSTs |
| **Path** | Whatever the subscriber registered (`callback_url`) | The MCP server POSTs to exactly this URL |
| **Source IP** | The MCP server's egress IPs | Restrict to known issuers |
| **Headers required** | `Content-Type: application/json` + the signature pair (see below) | Reject unsigned requests at the edge |
| **Headers (StandardWebhooks mode, default)** | `webhook-id` + `webhook-timestamp` + `webhook-signature` (`v1,<base64>`) | Per [standardwebhooks.com](https://www.standardwebhooks.com/) |
| **Headers (MCPHeaders mode, opt-in)** | `X-MCP-Signature` (`sha256=<hex>`) + `X-MCP-Timestamp` (unix seconds) | The pre-r3167245184 default; opt-in for callers that already wired against this shape |
| **Body size** | Reasonable cap (1 MB is plenty for almost any event payload) | Limit DoS surface |
| **Rate limit** | Per source IP, generous (≥ 100 r/s) | Bursts happen during reconnects + identity-mode rebroadcasts |

Don't reject based on User-Agent — mcpkit uses Go's default `net/http` UA which can change between versions.

## SSRF guards (server side)

`events.ValidateWebhookURL` runs at `events/subscribe` time and rejects obviously bad callback URLs. Today it checks:

- Scheme is `http` or `https`.
- **Warns** (does not reject) on `localhost` / `127.0.0.1` / `::1` / `0.0.0.0` — useful for dev/test.

For production, **add DNS resolution + private-IP rejection**:

- Resolve the URL's hostname to A/AAAA records at subscribe time.
- Reject if any resolved address falls in RFC 1918 private space (`10/8`, `172.16/12`, `192.168/16`), link-local (`169.254/16`), or IPv6 ULA (`fc00::/7`).
- Re-resolve on each delivery if you're paranoid about DNS rebinding (mcpkit's default `http.Client` does not do this).

The cheapest place to add this is by wrapping `ValidateWebhookURL` and configuring your own validator on the registry, or by patching the existing function for your fork. Until the spec settles, it's an operator responsibility.

## Retry and backoff timing

The default `WebhookRegistry.deliver` policy (in `webhook.go`):

| Field | Value |
|---|---|
| HTTP client timeout | 5 seconds per attempt |
| Initial backoff | 500 ms |
| Backoff growth | 2x per retry |
| Max backoff | 5 seconds |
| Max retries | 3 (so up to 4 attempts total) |
| Retry on | Network errors, HTTP 5xx |
| Don't retry on | HTTP 4xx (treated as receiver-side reject) |

Worst-case wall clock for a fully-failing delivery: roughly `5s + 0.5s + 5s + 1s + 5s + 2s + 5s ≈ 23s` (timeouts + backoffs + retries). Tune your WAF / proxy idle timeouts so they don't kill the connection inside this window.

**Receiver implications:**

- Receivers should be **idempotent**. The same event may arrive more than once if the receiver returned a 5xx and the retry succeeded. Deduplicate by `event.eventId` (always present, always unique per source).
- Returning **HTTP 4xx** stops retries. Use 4xx for "I will never accept this" (bad signature, malformed payload). Use 5xx for "try again" (transient infrastructure issue).
- Returning **HTTP 2xx fast** matters more than the body — the body is ignored. A WAF that buffers responses can hold up the delivery loop briefly but won't cause re-delivery if the upstream returned 2xx.

## TTL negotiation and refresh

Per spec PR1 commit `99f3589c` §"Subscription TTL", the wire shape is a client-suggested TTL the server grants or clamps:

- Request: `ttlMs` is optional and tristate. Absent → server picks. Numeric → suggested ms. Explicit `null` → request no-expiry.
- Response: `refreshBefore` is always present and nullable. An ISO 8601 string is a finite grant; JSON `null` is a no-expiry grant.

mcpkit's default TTL is 1 hour, clamped to the spec envelope **[5 min, 24 h]** at `WithWebhookTTL` registration time. Client suggestions outside the envelope are clamped UP to the floor (the spec's "one sanctioned exception") or DOWN to the ceiling. Tests and demos that need sub-minimum TTLs to drive SDK refresh behavior pass `WithUnsafeWebhookTTLBypass` — production deployments MUST NOT use the bypass. Per spec there is no rejection path for TTL values: clamping is self-announcing on the wire, so a client reading the granted `refreshBefore` just learns what was actually granted.

### No-expiry subscriptions

`WithAllowInfiniteWebhookTTL()` opts the registry into accepting `ttlMs: null` requests and returning `refreshBefore: null`. Default is OFF — without the option, `ttlMs: null` collapses to the server-default finite grant, which the client treats like any other finite refresh-before.

A server granting no-expiry accepts the spec's durability obligations:

- subscriptions persist until `events/unsubscribe` or server-initiated termination;
- subscriptions MUST persist across restarts (no refresh cycle to recover them otherwise — use a durable `WebhookStore` backend like Postgres);
- verification status MUST be persisted alongside the subscription (a persisted sub resumes delivery without a re-subscribe handshake);
- TTL expiry no longer GCs orphans — the server MAY drop after sustained delivery failure (failure-based GC) and SHOULD attempt a `terminated` envelope when it does.

The library wires the in-process pieces: prune loop skips no-expiry targets, `WebhookStore` round-trips the nil `ExpiresAt` sentinel, and the response emits `refreshBefore: null`.

**Failure-based GC.** Each delivery failure anchors a `FailingContinuouslySince` timestamp on the subscription (distinct from the existing `FailedSince` which is reset by the sliding suspend window — the GC anchor is **never** reset by quiet periods, only by a successful delivery). When a no-expiry subscription has been continuously failing for longer than `DefaultNoExpiryFailureGCWindow` (72 hours by default; tune via `WithNoExpiryFailureGCWindow(d time.Duration)`), the registry drops the subscription and POSTs a `terminated` envelope to the receiver as a courtesy notification. The drop fires through the same `PostTerminated` path as `events/unsubscribe` so on-remove hooks run and downstream stores release their per-subscription state. Finite-TTL subscriptions also accumulate `FailingContinuouslySince` for diagnostic purposes but are exempt from the GC trigger — their backstop remains TTL expiry.

**Construction-time validation.** Calling `WithAllowInfiniteWebhookTTL()` without also passing `WithWebhookStore(persistent)` triggers a stark warning at registry construction. No-expiry subscriptions held in the default in-memory store violate the spec's "persist across restarts" obligation — the warning points operators at the GORM-backed implementation as the supported choice. Dev/test setups can ignore it; production deployments must replace the default store.

**Persisted verification status** is the one remaining piece of the spec's no-expiry obligation list still pending — tracked under issue 490 (PostVerification rewrite covering all four verification paths). The current ext/events impl is verification-stub-only; once #490 lands, the stub becomes a real persisted field.

### Refresh loop

The shipped client SDKs handle TTL refresh automatically:

- **Go** (`experimental/ext/events/clients/go`): `Subscription` runs a background loop that re-calls `events/subscribe` at `RefreshFactor × (refreshBefore - now)` (default factor 0.5). For no-expiry grants the loop drops to a 1-hour health-check cadence (per spec: "Even with no expiry, clients SHOULD still re-call events/subscribe occasionally" for cursor advancement, `deliveryStatus` observation, and reactivation).
- **Python** (`experimental/ext/events/clients/python/events_client.py`): `WebhookSubscription` does the same finite-TTL refresh; no-expiry support is pending.

Both Go helpers handle the boundary race: if a refresh hits the registry just after the TTL expired (subscription not found), they immediately re-subscribe and fire the `OnRecover` callback.

**Operational implications:**

- The refresh traffic is `events/subscribe` calls on the MCP wire, **not** webhook traffic. Stateful firewalls between the subscriber and the MCP server need to allow that.
- If the network between the subscriber and the MCP server flaps, the refresh won't land and (for finite-TTL subs) the registry will GC the subscription. A new `events/subscribe` once connectivity returns is the recovery path; expect a small gap in deliveries. No-expiry subscriptions survive the flap because the registry doesn't GC them on TTL.
- Sizing: 1 hour default TTL × 0.5 refresh factor = one refresh call per subscription per 30 min. No-expiry subscriptions drop to one health-check per hour. With 10K active finite subscriptions this is ~5 r/s of subscribe traffic; with 10K no-expiry subs it's ~3 r/s.

## Webhook secret considerations

Per spec, the webhook signing secret is **client-supplied only** (`whsec_` + base64 of 24-64 random bytes per the Standard Webhooks profile). The server validates the format at `events/subscribe` time and stores the value as-is. The server does NOT generate or rotate secrets.

Operational notes:

- **Receiver and subscriber must agree on the secret.** If they're the same process (e.g., a forward proxy that subscribes on its own behalf), this is automatic. If they're different — e.g., proxy receives, app subscribes — the subscriber must communicate the secret to the proxy out-of-band. SDKs auto-generate by default; surface the value via your secrets-manager / proxy-config path.
- **Rotation is client-initiated.** Supply a new `whsec_` value on a refresh `events/subscribe` call. The server replaces the stored value; in-flight deliveries signed with the old secret will fail verification at the receiver. Spec describes a Standard-Webhooks dual-sign grace window for this case (not yet implemented in mcpkit).
- **Treat each `whsec_` value as a credential.** Provision via secrets manager (Vault, AWS Secrets Manager, GCP Secret Manager, K8s secret with appropriate restrictions) when subscribing programmatically. Compromise of one secret only compromises that subscription's deliveries — there's no master root.

## Auth and tuple subscription identity

Per spec §"Subscription Identity" → "Authentication required" L361, webhook `events/subscribe` and `events/unsubscribe` MUST require an authenticated principal — servers reject unauthenticated calls with `-32012 Forbidden`. The registry keys subscriptions on the canonical tuple `(principal, delivery.url, name, params)`; cross-tenant isolation is by construction since the principal is part of the key.

Production wiring (the spec-strict path):

```go
validator := auth.NewJWTValidator(auth.JWTConfig{
    JWKSURL:  "<your-OIDC-issuer>/.well-known/jwks.json",
    Issuer:   "<your-OIDC-issuer>",
    Audience: "mcp-events",
})
validator.Start()

srv := server.NewServer(
    core.ServerInfo{...},
    server.WithSubscriptions(),
    server.WithAuth(validator),  // ← anonymous webhook subscribes → -32012 Forbidden
)
events.Register(events.Config{
    Sources:  ...,
    Webhooks: webhooks,
    Server:   srv,
    // UnsafeAnonymousPrincipal intentionally NOT set — production
    // deployments rely on the spec-strict auth gate.
})
```

The `events` package only depends on `core.Claims` (the abstract auth contract), not on `ext/auth` or any specific auth implementation. You can swap in mTLS-derived principals, session-cookie validators, or custom JWKS — Events keeps working as long as `ctx.AuthClaims().Subject` returns the principal.

### `UnsafeAnonymousPrincipal` is for demos only

The `events.Config.UnsafeAnonymousPrincipal` field deliberately deviates from the spec — when set, anonymous calls are accepted under the configured principal. **Production deployments MUST leave this field empty.** The startup log line emitted by `events.Register` explicitly warns when it's non-empty so misconfiguration is loud rather than silent.

If a production deployment sets it: the spec's `-32012 Forbidden` rejection is bypassed; webhook subscribe accepts anonymous calls under a single shared principal; cross-tenant isolation breaks (everyone is "the demo user"); the audit trail loses its principal field. None of these are acceptable production properties.

The demos use it as an escape hatch so `make demo` works without standing up an auth provider. Each demo also auto-detects `OAUTH_ISSUER` and switches to real auth when present — see `examples/events/discord/README.md` for the env-var contract.

## Connecting an MCP host

Once the server is running, point any MCP host at it:

```bash
# Claude Code
claude mcp add events --transport streamable-http http://your-server/mcp

# Claude Desktop / VS Code (mcp.json)
{
  "mcpServers": {
    "events": {
      "type": "streamable-http",
      "url": "http://your-server/mcp"
    }
  }
}
```

For interactive demos, see the walkthroughs in [`examples/events/discord/`](../../../examples/events/discord/) and [`examples/events/telegram/`](../../../examples/events/telegram/).

## Quick checklist

Before going live with a webhook-enabled events server in a private-cloud deployment:

- [ ] Receiver behind WAF, allowlist rules in place (method/path/source-IP/headers).
- [ ] MCP server has outbound egress to receiver URLs.
- [ ] SSRF protection — DNS resolution + private-IP rejection wraps `ValidateWebhookURL`.
- [ ] WAF / proxy idle timeouts > 25 s to survive the worst-case retry chain.
- [ ] Receiver is idempotent on `event.eventId`.
- [ ] Receiver returns 2xx for accept, 4xx for reject-permanently, 5xx for retry.
- [ ] Webhook secrets (each subscription's `whsec_` value) reach the receiver via your secrets-management path; rotation procedure documented.
- [ ] **`events.Config.UnsafeAnonymousPrincipal` is EMPTY** in production code paths. Auth is wired via `server.WithAuth(...)`. Anonymous webhook subscribes return `-32012 Forbidden`.
- [ ] Server startup log shows `[events] WARNING: UnsafeAnonymousPrincipal=...` is **NOT** present. (Its presence indicates the demo escape hatch is on.)
- [ ] If using Standard Webhooks header mode, WAF allowlist has the `webhook-*` headers (not `X-MCP-*`).
- [ ] Subscribers run an SDK that auto-refreshes (or have explicit refresh logic that fires before `refreshBefore`).
