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
