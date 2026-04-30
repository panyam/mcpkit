# Telegram Events Example

Reference server demonstrating the [MCP Events spec](https://github.com/modelcontextprotocol/experimental-ext-triggers-events/pull/1) with Telegram as the event source. Built on the [`experimental/ext/events`](../../../experimental/ext/events/) library.

Companion to [Clare Liguori's TypeScript implementation](https://github.com/modelcontextprotocol/experimental-ext-triggers-events/tree/main/telegram-reference-server). Also a lighter mirror of the [discord-events demo](../discord/) — same protocol, different bot SDK.

## Walkthrough

```bash
make serve    # terminal 1 — real MCP server
make demo     # terminal 2 — scripted demokit walkthrough (TUI)
```

A condensed walkthrough focused on the telegram-specific payload shape and the typed Go SDK at [`experimental/ext/events/clients/go/`](../../../experimental/ext/events/clients/go/). Generated artifact in [`WALKTHROUGH.md`](WALKTHROUGH.md) — regenerate via `make readme`.

For the full protocol exposition (events/list, poll, secret modes, header modes, the spec's design rationale) see [`../discord/WALKTHROUGH.md`](../discord/WALKTHROUGH.md). This README intentionally skips repeating what's already in the walkthroughs.

> **Going to production?** See [`experimental/ext/events/DEPLOYMENT.md`](../../../experimental/ext/events/DEPLOYMENT.md) for private-cloud / WAF guidance.

## Setup — connecting to Telegram

The walkthrough runs in test mode by default (no Telegram needed). To wire up a real bot:

```bash
TELEGRAM_BOT_TOKEN=your-token make serve
```

Get a bot token from [@BotFather](https://t.me/BotFather) (`/newbot`).

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
