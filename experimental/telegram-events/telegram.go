// Package main implements a Telegram MCP Events reference server demonstrating
// push, poll, and webhook delivery modes using mcpkit. All event production
// goes through events.YieldingSource — the library owns storage, cursors, and
// fanout to push + webhook subscribers.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// TelegramEventData is the typed payload published to subscribers. Used as
// both the typed parameter to yield() and the wire shape on the events
// endpoints — single shape, no internal-vs-wire mismatch.
type TelegramEventData struct {
	ChatID    string `json:"chat_id"`
	MessageID string `json:"message_id" jsonschema:"description=Telegram message ID"`
	User      string `json:"user" jsonschema:"description=Sender username or first name"`
	Text      string `json:"text" jsonschema:"description=Message text content"`
	Timestamp string `json:"ts" jsonschema:"description=ISO 8601 timestamp,format=date-time"`
}

// telegramSenderName picks the best display name from a Telegram User.
func telegramSenderName(u *tgbotapi.User) string {
	if u == nil {
		return "unknown"
	}
	if u.UserName != "" {
		return u.UserName
	}
	return u.FirstName
}

// makeTelegramEvent builds a TelegramEventData payload from a Telegram
// message. Used by both the long-poll loop and the webhook receiver.
func makeTelegramEvent(msg *tgbotapi.Message) TelegramEventData {
	return TelegramEventData{
		ChatID:    strconv.FormatInt(msg.Chat.ID, 10),
		MessageID: strconv.Itoa(msg.MessageID),
		User:      telegramSenderName(msg.From),
		Text:      msg.Text,
		Timestamp: time.Unix(int64(msg.Date), 0).Format(time.RFC3339),
	}
}

// handleTelegramWebhook processes a Telegram Bot API webhook POST and yields
// the resulting event. Returns true if an event was published.
func handleTelegramWebhook(yield func(TelegramEventData) error, r *http.Request) bool {
	var update tgbotapi.Update
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		log.Printf("[telegram] failed to decode webhook: %v", err)
		return false
	}
	if update.Message == nil || update.Message.Text == "" {
		return false
	}
	if err := yield(makeTelegramEvent(update.Message)); err != nil {
		log.Printf("[telegram] yield failed: %v", err)
		return false
	}
	return true
}
