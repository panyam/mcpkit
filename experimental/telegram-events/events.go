package main

import (
	"fmt"
	"strconv"
	"time"

	"github.com/panyam/mcpkit/experimental/ext/events"
)

// TelegramEventData is the typed payload within a Telegram event.
// PayloadSchema is auto-derived from this struct via events.TypedSource.
type TelegramEventData struct {
	ChatID    string `json:"chat_id"`
	MessageID string `json:"message_id" jsonschema:"description=Telegram message ID"`
	User      string `json:"user" jsonschema:"description=Sender username or first name"`
	Text      string `json:"text" jsonschema:"description=Message text content"`
	Timestamp string `json:"ts" jsonschema:"description=ISO 8601 timestamp,format=date-time"`
}

// newTelegramSource creates an EventSource backed by the MessageStore.
// The payloadSchema is auto-derived from TelegramEventData.
func newTelegramSource(store *MessageStore) events.EventSource {
	return events.TypedSource[TelegramEventData](events.EventDef{
		Name:        "telegram.message",
		Description: "Fires when a message is received by the Telegram bot",
		Delivery:    []string{"push", "poll", "webhook"},
	}, func(cursor string, limit int) events.PollResult {
		var cursorInt int64
		if cursor != "" {
			cursorInt, _ = strconv.ParseInt(cursor, 10, 64)
		}

		pr := store.GetSince(cursorInt, limit)
		evts := make([]events.Event, 0, len(pr.Messages))
		for _, m := range pr.Messages {
			evts = append(evts, messageToEvent(m))
		}
		return events.PollResult{
			Events:    evts,
			Cursor:    strconv.FormatInt(pr.NextCursor, 10),
			CursorGap: pr.CursorGap,
		}
	})
}

// messageToEvent converts a Message to an events.Event envelope.
func messageToEvent(m Message) events.Event {
	return events.MakeEvent[TelegramEventData](
		"telegram.message",
		fmt.Sprintf("evt_%d", m.ID),
		strconv.FormatInt(m.ID, 10),
		m.Timestamp,
		TelegramEventData{
			ChatID:    strconv.FormatInt(m.ChatID, 10),
			MessageID: strconv.FormatInt(m.ID, 10),
			User:      m.Sender,
			Text:      m.Text,
			Timestamp: m.Timestamp.Format(time.RFC3339),
		},
	)
}
