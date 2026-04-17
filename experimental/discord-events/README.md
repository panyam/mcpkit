# Discord Events Example

Reference server demonstrating the [MCP Events spec](https://github.com/modelcontextprotocol/experimental-ext-triggers-events/pull/1) with Discord as the event source. Built on the [`experimental/ext/events`](../ext/events/) library.

Companion to the [Telegram example](../telegram-events/) — shows the events library handles structurally different payloads (Discord has nested author objects, embeds, threads, mentions vs Telegram's flat text model).

## Quick Start

```bash
# Test mode (no Discord — use make inject)
make run

# With Discord bot
DISCORD_BOT_TOKEN=your-token make run
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

## Injecting Messages

```bash
make inject TEXT="hello world"
make inject TEXT="test" SENDER="alice" CHANNEL="general"
```

## Diagnostics

```bash
make diag    # init → tools/list → events/list → events/poll → resources
make test    # Go tests
```

## Event Payload Shape

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

## Make Targets

| Target | Description |
|--------|-------------|
| `make run` | Start server (with bot if `DISCORD_BOT_TOKEN` set) |
| `make test` | Go tests |
| `make diag` | Full diagnostic sequence |
| `make inject TEXT="..."` | Inject a message (optional: `SENDER=`, `CHANNEL=`, `GUILD=`) |
