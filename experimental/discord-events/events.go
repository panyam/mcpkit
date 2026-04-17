package main

import (
	"fmt"
	"strconv"
	"time"

	"github.com/panyam/mcpkit/experimental/ext/events"
)

// DiscordEventData has a richer structure than Telegram — embeds, attachments,
// thread info, and message type. Shows the events library handles diverse
// payload shapes, not just flat text.
type DiscordEventData struct {
	GuildID   string              `json:"guild_id" jsonschema:"description=Discord server (guild) ID"`
	ChannelID string              `json:"channel_id" jsonschema:"description=Channel ID where the message was sent"`
	MessageID string              `json:"message_id" jsonschema:"description=Discord message snowflake ID"`
	Author    DiscordAuthor       `json:"author" jsonschema:"description=Message author"`
	Content   string              `json:"content" jsonschema:"description=Text content of the message"`
	Type      string              `json:"type" jsonschema:"description=Message type: default reply thread_starter,enum=default,enum=reply,enum=thread_starter"`
	Thread    *DiscordThread      `json:"thread,omitempty" jsonschema:"description=Thread context if this message is part of a thread"`
	Embeds    []DiscordEmbed      `json:"embeds,omitempty" jsonschema:"description=Rich embeds attached to the message"`
	Mentions  []string            `json:"mentions,omitempty" jsonschema:"description=Usernames mentioned in the message"`
	Timestamp string              `json:"ts" jsonschema:"description=ISO 8601 timestamp,format=date-time"`
}

type DiscordAuthor struct {
	ID       string `json:"id" jsonschema:"description=User snowflake ID"`
	Username string `json:"username"`
	Bot      bool   `json:"bot,omitempty" jsonschema:"description=True if the author is a bot"`
}

type DiscordThread struct {
	ID       string `json:"id" jsonschema:"description=Thread channel ID"`
	Name     string `json:"name" jsonschema:"description=Thread name"`
	ParentID string `json:"parent_id" jsonschema:"description=Parent channel ID"`
}

type DiscordEmbed struct {
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	URL         string `json:"url,omitempty"`
}

// newDiscordSource creates an EventSource backed by the MessageStore.
// PayloadSchema is auto-derived from DiscordEventData — nested structs,
// optional fields, enums all reflected in the JSON Schema.
func newDiscordSource(store *MessageStore) events.EventSource {
	return events.TypedSource[DiscordEventData](events.EventDef{
		Name:        "discord.message",
		Description: "Fires when a message is sent in a Discord channel the bot can see",
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

func messageToEvent(m Message) events.Event {
	return events.MakeEvent[DiscordEventData](
		"discord.message",
		fmt.Sprintf("evt_%d", m.ID),
		strconv.FormatInt(m.ID, 10),
		m.Timestamp,
		DiscordEventData{
			GuildID:   m.GuildID,
			ChannelID: m.ChannelID,
			MessageID: strconv.FormatInt(m.ID, 10),
			Author: DiscordAuthor{
				Username: m.Sender,
			},
			Content:   m.Text,
			Type:      "default",
			Timestamp: m.Timestamp.Format(time.RFC3339),
		},
	)
}
