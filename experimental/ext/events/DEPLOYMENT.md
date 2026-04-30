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
| **Headers (MCPHeaders mode, default)** | `X-MCP-Signature` (`sha256=<hex>`) + `X-MCP-Timestamp` (unix seconds) | The two-field signature pair |
| **Headers (StandardWebhooks mode)** | `webhook-id` + `webhook-timestamp` + `webhook-signature` (`v1,<base64>`) | Per [standardwebhooks.com](https://www.standardwebhooks.com/) |
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

## TTL refresh as keepalive

Webhook subscriptions are soft state. The default TTL is 60 seconds (`WithWebhookTTL` overrides). Subscribers MUST re-subscribe before the TTL expires or the registry evicts them and deliveries stop.

The shipped client SDKs handle this automatically:

- **Go** (`experimental/ext/events/clients/go`): `Subscription` runs a background loop that re-calls `events/subscribe` at `RefreshFactor × TTL` (default 0.5).
- **Python** (`experimental/ext/events/clients/python/events_client.py`): `WebhookSubscription` does the same.

Both helpers handle the boundary race: if a refresh hits the registry just after the TTL expired (subscription not found), they immediately re-subscribe and fire the `OnRecover` callback.

**Operational implications:**

- The refresh traffic is `events/subscribe` calls on the MCP wire, **not** webhook traffic. Stateful firewalls between the subscriber and the MCP server need to allow that.
- If the network between the subscriber and the MCP server flaps, the refresh won't land and the registry will GC the subscription. A new `events/subscribe` once connectivity returns is the recovery path; expect a small gap in deliveries.
- Sizing: 60 s default TTL × 0.5 refresh factor = one refresh call per subscription per 30 s. With 10K active subscriptions this is ~333 r/s of subscribe traffic — small but not zero.

## Identity-mode considerations

When running the registry with `WithWebhookSecretMode(events.WebhookSecretIdentity)`, the secret each subscription gets is derived from a server-side master root (`WithWebhookRoot`). Treat the root like a master credential:

- Provision via secrets manager (Vault, AWS Secrets Manager, GCP Secret Manager, K8s secret with appropriate restrictions) — **not** an env var checked into config.
- **Rotate by deploying a new root.** All derived secrets rotate atomically. Plan for a brief overlap window where receivers may need to accept either old or new — the spec doesn't define a graceful rotation and this is operator territory.
- The root file should be readable only by the MCP server process. Compromise of the root means compromise of every derived subscription secret.

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
- [ ] If using Identity mode, master root is in a secrets manager and rotation plan documented.
- [ ] If using Standard Webhooks header mode, WAF allowlist has the `webhook-*` headers (not `X-MCP-*`).
- [ ] Subscribers run an SDK that auto-refreshes (or have explicit refresh logic that fires before `refreshBefore`).
