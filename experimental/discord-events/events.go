package main

import (
	"github.com/panyam/mcpkit/experimental/ext/events"
)

// newDiscordSource constructs the YieldingSource for discord.message events.
// The library owns the in-memory ring buffer; the resource handlers read
// typed payloads back via source.Recent / source.ByCursor — single source of
// truth, no duplication, no resource-side unmarshaling.
func newDiscordSource() (*events.YieldingSource[DiscordEventData], func(DiscordEventData) error) {
	return events.NewYieldingSource[DiscordEventData](events.EventDef{
		Name:        "discord.message",
		Description: "Fires when a message is sent in a Discord channel the bot can see",
		Delivery:    []string{"push", "poll", "webhook"},
	}, events.WithMaxSize(1000))
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
