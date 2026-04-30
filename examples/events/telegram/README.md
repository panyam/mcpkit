# Telegram Events Example

Reference server demonstrating the [MCP Events spec](https://github.com/modelcontextprotocol/experimental-ext-triggers-events/pull/1) with Telegram as the event source. Built on the [`experimental/ext/events`](../../../experimental/ext/events/) library.

Companion to [Clare Liguori's TypeScript implementation](https://github.com/modelcontextprotocol/experimental-ext-triggers-events/tree/main/telegram-reference-server). Also a lighter mirror of the [discord-events demo](../discord/) — same protocol, different bot SDK.

## Walkthrough

A condensed walkthrough focused on the telegram-specific payload shape and the typed Go SDK at [`experimental/ext/events/clients/go/`](../../../experimental/ext/events/clients/go/). Two ways to run it.

### Option A — Test mode (no Telegram token needed)

All walkthrough steps run; the final live-interaction step skips with a "no token" message.

```bash
make serve    # terminal 1 — server in test mode
make demo     # terminal 2 — walkthrough
```

To simulate Telegram activity from a third terminal:

```bash
make inject TEXT="hello world"   # message event (cursored)
make inject-typing               # typing indicator (cursorless, demo-only — see below)
```

### Option B — Real bot mode (requires `TELEGRAM_BOT_TOKEN`)

Same walkthrough plus the final live step captures real message events from a chat with the bot.

```bash
TELEGRAM_BOT_TOKEN=your-token make serve   # terminal 1 — server in bot mode
make demo                                   # terminal 2 — walkthrough
# When the live step starts, send a message to your bot in Telegram.
```

**Note on typing events**: Telegram's Bot API doesn't expose user typing events to bots — only the bot can send typing chat actions, not the other way around. So `make inject-typing` works as a demo of the cursorless wire shape, but Option B can't capture real typing events. Discord can; see [`../discord/WALKTHROUGH.md`](../discord/WALKTHROUGH.md) for the live-typing demo.

Generated walkthrough in [`WALKTHROUGH.md`](WALKTHROUGH.md) — regenerate via `make readme`. For the full protocol exposition (events/list, poll, secret modes, header modes, the spec's design rationale) see [`../discord/WALKTHROUGH.md`](../discord/WALKTHROUGH.md). This README intentionally skips repeating what's already in the walkthroughs.

> **Going to production?** See [`experimental/ext/events/DEPLOYMENT.md`](../../../experimental/ext/events/DEPLOYMENT.md) for private-cloud / WAF guidance.

## Setup — getting a Telegram bot token (Option B only)

Skip this section if you're running in test mode (Option A above).

Get a bot token from [@BotFather](https://t.me/BotFather) (`/newbot`). That's it — no privileged-intent toggles like Discord has.

## Architecture

```
Telegram Bot (long-poll)  ──or──  POST /inject
                │                       │
                ▼                       ▼
                yield(TelegramEventData{...})    ← user code: one call
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
    source.Recent(50)         → []TelegramEventData
    source.ByCursor("42")     → (TelegramEventData, true)
```

## Server flag examples (outside the walkthrough)

The walkthrough runs against the default server config. To exercise the other modes, pass flags to `make serve`:

```bash
# Identity mode — secret = HMAC(root, tuple); subscribe is idempotent
go run . --serve -webhook-secret-mode identity -webhook-root deadbeefcafef00d

# Opt out of the Standard Webhooks default back to X-MCP-* headers
go run . --serve -webhook-header-mode mcp
```

Full mode matrix in [`experimental/ext/events/README.md`](../../../experimental/ext/events/README.md).

## Make targets

| Target | Description |
|--------|-------------|
| `make serve` | Start the server (with bot if `TELEGRAM_BOT_TOKEN` set; test mode otherwise) |
| `make demo` | Run the demokit walkthrough — `--tui` mode |
| `make readme` | Regenerate `WALKTHROUGH.md` from the demo step definitions |
| `make build` | Build the binary |
| `make test` | Go tests |
| `make inject TEXT="..."` | Inject a message event (optional: `SENDER=`, `CHAT_ID=`) |
| `make inject-typing` | Inject a cursorless typing event (optional: `USER_NAME=`, `CHAT_ID=`) |
| `make list` | Show server capabilities via Python client |
| `make listen` | Python SSE push listener |
| `make webhook` | Python webhook receiver — subscribe + auto-refresh, receive HMAC-signed POSTs |
| `make poll` | Python polling loop (default 5s interval, override: `INTERVAL=10`) |

All Python clients share the [`events_client.py`](../../../experimental/ext/events/clients/python/events_client.py) helper.
