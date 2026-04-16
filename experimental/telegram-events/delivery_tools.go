package main

import (
	"fmt"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// ToolDelivery registers MCP tools for Telegram actions (send_message).
// Event delivery (poll, push, webhook) is handled by MethodDelivery via
// protocol-level methods per Peter Alexander's design sketch.
type ToolDelivery struct {
	Bot *tgbotapi.BotAPI // nil in test mode
}

type sendMessageInput struct {
	ChatID int64  `json:"chat_id" jsonschema:"description=Telegram chat ID to send the message to.,required"`
	Text   string `json:"text" jsonschema:"description=Message text to send.,required"`
}

// Register hooks up MCP tools for Telegram actions.
func (d *ToolDelivery) Register(srv *server.Server, store *MessageStore, webhooks *WebhookRegistry) {
	// Send tool: send_message(chat_id, text) → confirmation
	srv.Register(core.TextTool[sendMessageInput](
		"send_message",
		"Send a text message to a Telegram chat.",
		func(ctx core.ToolContext, input sendMessageInput) (string, error) {
			if d.Bot == nil {
				return "", fmt.Errorf("telegram bot not configured (running in test mode)")
			}
			chatMsg := tgbotapi.NewMessage(input.ChatID, input.Text)
			sent, err := d.Bot.Send(chatMsg)
			if err != nil {
				return "", fmt.Errorf("telegram send failed: %w", err)
			}
			return fmt.Sprintf("sent (id: %d)", sent.MessageID), nil
		},
	))
}
