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
