# integrations/roomsevents: implementation notes

## Why this is its own module, outside `ext`

Constraint C4 refuses a direct require from one extension module of another, and decides what an
extension is by an `ext` path segment. The bridge had to become an events server for its rooms
(#1581), which needs both experimental/ext/rooms and experimental/ext/events. Neither may import the
other, and a nested `experimental/ext/rooms/events` module would trip C4 too. So the bridge got a
hook (`rooms.WithPageEvents`) and two questions (`Attached`, `PageAgreed`), and this module, with no
`ext` in its path, joins them. It is C4's documented escape hatch, a separate module importing both,
used for shipping code rather than a test. It's tagged, since agni imports it.

## Who hears a page

The rule is the bridge's own agreement, applied to events. A subscription must come from a session
attached to the room (checked in `OnSubscribe`), and each event goes only to sessions the emitting
page agreed to (checked in `Match`). Without the second check, a page alice opened would talk to
bob's agent the moment bob attached, though alice never accepted his code. Detach ends agreement, so
the `Attached` test in `Match` is a second line that no test isolates; the agreement test covers a
detached session already.

`Match` runs on a background context for push, with no MCP session in it. `OnSubscribe` gets the
request's context, so the adapter records which session opened each stream (by subscription id) and
looks it up there.

## A refused stream is refused up front

experimental/ext/events used to send `notifications/events/active` before it ran `OnSubscribe`, so a
stream opened before `attach` was confirmed and then ended with `-32025`. Since #1603 the hook runs
first, and the attach check wraps `events.ErrSubscribeForbidden`, so the client's `Stream` fails at
once with `-32024`.

## The channel contract, and what was checked

From code.claude.com/docs/en/channels-reference and /mcp, read 2026-10-23:

- `capabilities.experimental["claude/channel"] = {}` makes a server a channel. mcpkit had no
  `experimental` field on `ServerCapabilities`, although the spec defines one, so this PR added it
  with `server.WithExperimentalCapability`, rather than faking it with `Extension.TopLevel`. That
  would also have written a spurious `extensions["claude/channel"]`.
- `notifications/claude/channel` takes `{content: string, meta: Record<string, string>}`. Meta keys
  that aren't identifiers are dropped silently, so every key here is one (`event_id`, not
  `event-id`).
- Claude Code starts the server as a stdio subprocess. A custom channel needs
  `--dangerously-load-development-channels server:<name>`, interactively.
- On the v2 runtime a channel server negotiating 2026-07-28 isn't registered. mcpkit servers offer
  2026-07-28 first by default, so the front end pins `WithSupportedVersions` to earlier revisions.
- No minimum Claude Code version for channels is stated on those pages. #1581's "v2.1.80+" is
  unverified.

The test drives the front end over a pipe as Claude Code would. Nothing here has run inside a real
Claude Code session.

## Pushing from a stdio server

The stdio transport registers no session broadcaster, so `Server.Broadcast` never reaches Claude
Code. The front end keeps the detached context of the first tool call it serves
(`core.DetachForBackground`) and pushes through that. In practice that call is `attach`, and
nothing arrives before it anyway.

## Dedup

Every stream shares one `eventsclient.Deduper` (#1582), so the replay after a stream reconnects
reaches the session once. The test drives a push stream and a poll backfill from an earlier cursor
into the same sink.
