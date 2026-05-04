package main

import (
	"github.com/panyam/mcpkit/experimental/ext/events"
)

// newDiscordSource constructs the YieldingSource for discord.message events.
// The library owns the in-memory ring buffer; the resource handlers read
// typed payloads back via source.Recent / source.ByCursor — single source of
// truth, no duplication, no resource-side unmarshaling.
//
// δ-4: the source attaches per-event `_meta` derived from the payload
// (spec follow-on commit d4faef9 2026-05-01). channel_type and mention_count
// are app-defined classifications that don't fit `data` — exactly what
// `_meta` is for. Receivers see them under the spec-canonical `_meta` key.
func newDiscordSource() (*events.YieldingSource[DiscordEventData], func(DiscordEventData) error) {
	src, yield := events.NewYieldingSource[DiscordEventData](events.EventDef{
		Name:        "discord.message",
		Description: "Fires when a message is sent in a Discord channel the bot can see",
		Delivery:    []string{"push", "poll", "webhook"},
		Meta:        map[string]any{"category": "messaging"},
	}, events.WithMaxSize(1000))
	src.SetMetaFunc(func(d DiscordEventData) map[string]any {
		channelType := "guild"
		if d.GuildID == "" {
			channelType = "dm"
		}
		return map[string]any{
			"channel_type":  channelType,
			"mention_count": len(d.Mentions),
		}
	})
	return src, yield
}

// newDiscordTypingSource constructs the cursorless YieldingSource for
// discord.typing events. Typing indicators are ephemeral — the source skips
// buffering and events emit with `cursor: null` on the wire. Push and webhook
// delivery still work; poll always returns empty (subscribers can't replay
// missed indicators, which matches the semantics of the underlying state).
func newDiscordTypingSource() (*events.YieldingSource[DiscordTypingData], func(DiscordTypingData) error) {
	return events.NewYieldingSource[DiscordTypingData](events.EventDef{
		Name:        "discord.typing",
		Description: "Fires when a user starts typing in a Discord channel",
		Delivery:    []string{"push", "webhook"},
	}, events.WithoutCursors())
}
