package main

import (
	"github.com/panyam/mcpkit/experimental/ext/events"
)

// newTelegramSource constructs the YieldingSource for telegram.message
// events. The library owns the in-memory ring buffer; the resource handlers
// read typed payloads back via source.Recent / source.ByCursor — single
// source of truth, no duplication, no resource-side unmarshaling.
func newTelegramSource() (*events.YieldingSource[TelegramEventData], func(TelegramEventData) error) {
	return events.NewYieldingSource[TelegramEventData](events.EventDef{
		Name:        "telegram.message",
		Description: "Fires when a message is received by the Telegram bot",
		Delivery:    []string{"push", "poll", "webhook"},
	}, events.WithMaxSize(1000))
}

// newTelegramTypingSource constructs the cursorless YieldingSource for
// telegram.typing events. Typing indicators are ephemeral — the source skips
// buffering and events emit with `cursor: null` on the wire. Push and webhook
// delivery still work; poll always returns empty.
func newTelegramTypingSource() (*events.YieldingSource[TelegramTypingData], func(TelegramTypingData) error) {
	return events.NewYieldingSource[TelegramTypingData](events.EventDef{
		Name:        "telegram.typing",
		Description: "Fires when a user starts typing in a Telegram chat",
		Delivery:    []string{"push", "webhook"},
	}, events.WithoutCursors())
}
