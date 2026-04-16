package main

import (
	"github.com/panyam/mcpkit/server"
)

// TelegramEvent is the MCP-facing event envelope, matching Clare's TS schema.
// Used by both push notifications and poll responses.
type TelegramEvent struct {
	EventID   string            `json:"eventId"`
	Name      string            `json:"name"`
	Timestamp string            `json:"timestamp"`
	Data      TelegramEventData `json:"data"`
	Cursor    string            `json:"cursor"`
}

// TelegramEventData is the payload within a TelegramEvent.
type TelegramEventData struct {
	ChatID    string `json:"chat_id"`
	MessageID string `json:"message_id"`
	User      string `json:"user"`
	Text      string `json:"text"`
	Timestamp string `json:"ts"`
}

// EventDelivery abstracts the three delivery modes (push, poll, webhook) so
// the server can swap between tool-based (today) and protocol-method-based
// (after issue #266 lands custom method handler support).
type EventDelivery interface {
	// Register hooks up the delivery endpoints on the MCP server. For tool-based
	// delivery this registers MCP tools; for method-based delivery this will
	// register custom JSON-RPC method handlers.
	Register(srv *server.Server, store *MessageStore, webhooks *WebhookRegistry)
}
