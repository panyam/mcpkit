# Discord Events Example

Reference server demonstrating the [MCP Events spec](https://github.com/modelcontextprotocol/experimental-ext-triggers-events/pull/1) with Discord as the event source. Built on the [`experimental/ext/events`](../../../experimental/ext/events/) library.

Companion to the [Telegram example](../telegram/) — shows the events library handles structurally different payloads (Discord has nested author objects, embeds, threads, mentions vs Telegram's flat text model).

## Walkthrough

The canonical demo for the events extension. Two ways to run it.

### Option A — Test mode (no Discord token needed)

All walkthrough steps run; the final live-interaction step skips with a "no token" message. Great for a quick read-through of the protocol features.

```bash
make serve    # terminal 1 — server in test mode
make demo     # terminal 2 — walkthrough
```

To simulate Discord activity (so you can see push/poll/webhook fanout in real time), inject events from a third terminal:

```bash
make inject TEXT="hello world"   # message event (cursored)
make inject-typing               # typing indicator (cursorless)
```

### Option B — Real bot mode (requires `DISCORD_BOT_TOKEN`)

Same walkthrough plus the final live step captures real typing + message events from the Discord channel where you invited the bot.

```bash
DISCORD_BOT_TOKEN=your-token make serve   # terminal 1 — server in bot mode
make demo                                  # terminal 2 — walkthrough
# When the live step starts, go type in your Discord channel.
```

See [`WALKTHROUGH.md`](WALKTHROUGH.md) for the full sequence diagram and step-by-step explanation. The walkthrough is generated from the demo step definitions in `walkthrough.go` — run `make readme` to regenerate.

> **Going to production?** See [`experimental/ext/events/DEPLOYMENT.md`](../../../experimental/ext/events/DEPLOYMENT.md) for private-cloud / WAF guidance.

## What it demonstrates

Each bullet maps to a step in the demokit walkthrough:

- **events/list** — the source catalog, including the `cursorless` flag.
- **Push** — long-lived `events/stream` SSE; `notifications/events/event` arrives in real time.
- **Poll** — single-subscription `events/poll` (multi-sub batching is not supported).
- **Cursorless source** — typing indicators wired as `cursor: null`; subscribers see live events but can't replay.
- **Webhook + auto-refresh** — `events/subscribe` driven via the typed `Subscription` + `Receiver[Data]` from `clients/go`, including HMAC verification and 0.5×TTL re-subscribe.
- **Source-side health signals** — `YieldError(err)` (transient → `notifications/events/error`) and `YieldTerminated(err)` (terminal → `notifications/events/terminated`).
- **Spec validation** — empty / malformed `delivery.secret` rejected with `-32602`; client-supplied `id` rejected; valid `whsec_` accepted with no secret echoed in the response.
- **Live Discord interaction** — real typing + message events from a Discord channel (when started with `DISCORD_BOT_TOKEN`).

## Setup — getting a Discord bot token (Option B only)

Skip this section if you're running in test mode (Option A above).

### Getting a Discord Bot Token

1. Go to https://discord.com/developers/applications
2. Click **New Application**, name it (e.g., "MCPKit Events")
3. Go to **Bot** tab → click **Reset Token** → copy the token
4. Under **Privileged Gateway Intents**, enable **Message Content Intent**
5. Go to **OAuth2** → **URL Generator**:
   - Scopes: `bot`
   - Bot Permissions: `Send Messages`, `Read Message History`
6. Copy the generated URL and open it to invite the bot to your server

The typing-indicator step in the walkthrough additionally needs the `IntentsGuildMessageTyping` intent — the server requests it automatically when started with `-token`, no extra dev-portal toggle required (typing isn't classified as a privileged intent).

## Architecture

```
Discord Bot (WebSocket)  ──or──  POST /inject
                │                       │
                ▼                       ▼
                yield(DiscordEventData{...})    ← user code: one call
                │
                │  YieldingSource (library):
                │    - assigns cursor + event ID
                │    - stores in bounded ring (1000 max)
                │    - calls library-installed fanout hook
                ▼
                ├──► events.Emit()              → Push (SSE broadcast)
                └──► events.EmitToWebhooks()     → Webhook (HMAC POST)
                                                   ▲
                                              events/poll reads from
                                              the same source's buffer

  Resource handlers read typed payloads via:
    source.Recent(50)         → []DiscordEventData
    source.ByCursor("42")     → (DiscordEventData, true)
```

The discord callback writes one line — `yield(...)` — and the library handles cursor assignment, retention, push fanout, webhook fanout, and typed read access. The YieldingSource's internal buffer is the single source of truth, exposed as both an `EventSource` (for `events/poll`) and as typed accessors (for resource reads).

## Event payload shape

Discord events have a richer structure than Telegram — nested author, optional threads, embeds, and mentions. The `payloadSchema` in `events/list` is auto-derived from the Go struct:

```json
{
  "guild_id": "123456",
  "channel_id": "789012",
  "message_id": "evt_1",
  "author": { "id": "111", "username": "alice", "bot": false },
  "content": "hello world",
  "type": "default",
  "thread": { "id": "999", "name": "discussion", "parent_id": "789012" },
  "embeds": [{ "title": "Link Preview", "url": "https://..." }],
  "mentions": ["bob", "carol"],
  "ts": "2026-04-16T12:00:00Z"
}
```

## Server flag examples (outside the walkthrough)

The walkthrough runs against the default server config. To exercise the other modes, pass flags to `make serve`:

```bash
# Opt out of the Standard Webhooks default back to legacy X-MCP-* headers
go run . --serve -webhook-header-mode mcp

# Drive a short TTL to watch the SDK's auto-refresh behavior in real time
go run . --serve -webhook-ttl 5s
```

Per spec, the webhook signing secret is **client-supplied only** (`whsec_` + base64 of 24-64 random bytes). The python `make webhook` and the Go SDK both auto-generate when the application doesn't supply one. See [`experimental/ext/events/README.md`](../../../experimental/ext/events/README.md) for the full configuration reference.

## Auth posture (γ): demo escape vs real OIDC

Per spec §"Subscription Identity" L361 webhook subscribe MUST require an authenticated principal. The demo auto-detects which posture to run in based on environment variables:

```bash
# Demo posture (default): no env vars set.
# Server runs anonymous; events.Config.UnsafeAnonymousPrincipal="demo-user"
# is the escape hatch so make demo works end-to-end without an OIDC provider.
# Server logs:  [server] auth: demo (anonymous → UnsafeAnonymousPrincipal)
make serve

# Real-OIDC posture: set OAUTH_ISSUER (and optionally OAUTH_AUDIENCE / OAUTH_JWKS_URL).
# Server wires server.WithAuth(JWTValidator) and follows the spec strictly —
# anonymous webhook subscribes are rejected with -32012 Unauthorized.
# Server logs:  [server] auth: real OIDC (...) — anonymous webhook subscribes rejected per spec
OAUTH_ISSUER=http://localhost:8081/realms/demo \
OAUTH_AUDIENCE=mcp-events \
make serve
```

Recognized env vars:

| Env var | Default | Notes |
|---|---|---|
| `OAUTH_ISSUER` | (unset → demo posture) | OIDC issuer URL. For Keycloak: `http://localhost:8081/realms/<realm>`. Setting this enables real auth. |
| `OAUTH_JWKS_URL` | `<issuer>/protocol/openid-connect/certs` | Override for non-Keycloak providers. |
| `OAUTH_AUDIENCE` | `mcp-events` | Tokens MUST have this audience claim. |

The events package itself depends only on `core.Claims` (the abstract auth contract), not on any specific auth implementation — see [`experimental/ext/events/README.md`](../../../experimental/ext/events/README.md) "Auth + extension composition" for the design rationale.

## Where to look in the code

- `examples/events/discord/main.go:serve` — server bootstrap (auth posture, source registration, `/inject` side endpoint via `server.WithMux`).
- `examples/events/discord/walkthrough.go:runDemo` — demokit script driving every step in the bullet list above.
- `examples/events/discord/events.go:newDiscordSource` — `events.YieldingSource[DiscordEventData]` construction (cursored).
- `examples/events/discord/events.go:newDiscordTypingSource` — cursorless typing source via `events.WithoutCursors()`.
- `examples/events/discord/handlers.go:registerResources` — typed resource handlers backed by the source's buffer.
- `examples/events/discord/handlers.go:registerTools` — `discord.send_message` tool.
- `experimental/ext/events/clients/go/subscription.go:Subscribe` — Go SDK subscribe + auto-refresh helper used in step 6.
- `experimental/ext/events/clients/go/receiver.go:NewReceiver` — typed inbound webhook receiver with HMAC verification.
- `experimental/ext/events/webhook.go:WebhookRegistry` — per-subscription delivery + signing + retry.

## Make targets

| Target | Description |
|--------|-------------|
| `make serve` | Start the server (with bot if `DISCORD_BOT_TOKEN` set; test mode otherwise) |
| `make demo` | Run the demokit walkthrough — `--tui` mode |
| `make readme` | Regenerate `WALKTHROUGH.md` from the demo step definitions |
| `make build` | Build the binary |
| `make test` | Go tests |
| `make test-ttl` | Drive the Python `WebhookSubscription` auto-refresh helper end-to-end (POSIX-only — see Makefile for Windows steps) |
| `make inject TEXT="..."` | Inject a message event (optional: `SENDER=`, `CHANNEL=`, `GUILD=`) |
| `make inject-typing` | Inject a cursorless typing event (optional: `USER_NAME=`, `CHANNEL=`, `GUILD=`) |
| `make list` | Show server capabilities via Python client (tools, resources, events, sample poll) |
| `make listen` | Python SSE push listener |
| `make webhook` | Python webhook receiver — subscribe + auto-refresh, receive HMAC-signed POSTs |
| `make poll` | Python polling loop (default 5s interval, override: `INTERVAL=10`) |

The Python clients (`make list / listen / webhook / poll`) are convenient for ad-hoc poking. The walkthrough above (`make demo`) is the canonical tour. Both share the [`events_client.py`](../../../experimental/ext/events/clients/python/events_client.py) helper.
