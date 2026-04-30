# Discord Events Example

Reference server demonstrating the [MCP Events spec](https://github.com/modelcontextprotocol/experimental-ext-triggers-events/pull/1) with Discord as the event source. Built on the [`experimental/ext/events`](../../../experimental/ext/events/) library.

Companion to the [Telegram example](../telegram/) — shows the events library handles structurally different payloads (Discord has nested author objects, embeds, threads, mentions vs Telegram's flat text model).

## Walkthrough

The canonical demo for the events extension. Runs end-to-end, hits every protocol feature.

```bash
make serve    # terminal 1 — real MCP server
make demo     # terminal 2 — scripted demokit walkthrough (TUI)
```

See [`WALKTHROUGH.md`](WALKTHROUGH.md) for the full sequence diagram and step-by-step explanation. Don't repeat it here — the walkthrough is generated from the demo step definitions in `walkthrough.go`, run `make readme` to regenerate.

> **Going to production?** See [`experimental/ext/events/DEPLOYMENT.md`](../../../experimental/ext/events/DEPLOYMENT.md) for private-cloud / WAF guidance.

## Setup — connecting to Discord

The walkthrough runs in test mode by default (no Discord needed). To wire up a real bot:

```bash
DISCORD_BOT_TOKEN=your-token make serve
```

### Getting a Discord Bot Token

1. Go to https://discord.com/developers/applications
2. Click **New Application**, name it (e.g., "MCPKit Events")
3. Go to **Bot** tab → click **Reset Token** → copy the token
4. Under **Privileged Gateway Intents**, enable **Message Content Intent**
5. Go to **OAuth2** → **URL Generator**:
   - Scopes: `bot`
   - Bot Permissions: `Send Messages`, `Read Message History`
6. Copy the generated URL and open it to invite the bot to your server

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
# Identity mode — secret = HMAC(root, tuple); subscribe is idempotent
go run . --serve -webhook-secret-mode identity -webhook-root deadbeefcafef00d

# Client-supplied secrets (echoed back if non-empty, server-generated if empty)
go run . --serve -webhook-secret-mode client

# Opt out of the Standard Webhooks default back to X-MCP-* headers
go run . --serve -webhook-header-mode mcp
```

Full mode matrix in [`experimental/ext/events/README.md`](../../../experimental/ext/events/README.md).

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
