// Package main implements a Discord MCP Events reference server demonstrating
// push, poll, and webhook delivery modes using mcpkit.
//
// All event production goes through events.YieldingSource — the library owns
// storage, cursor assignment, and fan-out to push + webhook subscribers. The
// demo wires Discord (or the /inject endpoint) to a single yield() call.
package main

import (
	"log"
	"time"
)

// DiscordEventData is the wire-format payload emitted to subscribers. It's
// also used directly as the typed parameter to the Discord callback — the
// store and the wire shape are the same.
type DiscordEventData struct {
	GuildID   string         `json:"guild_id" jsonschema:"description=Discord server (guild) ID"`
	ChannelID string         `json:"channel_id" jsonschema:"description=Channel ID where the message was sent"`
	MessageID string         `json:"message_id" jsonschema:"description=Discord message snowflake ID"`
	Author    DiscordAuthor  `json:"author" jsonschema:"description=Message author"`
	Content   string         `json:"content" jsonschema:"description=Text content of the message"`
	Type      string         `json:"type" jsonschema:"description=Message type: default reply thread_starter,enum=default,enum=reply,enum=thread_starter"`
	Thread    *DiscordThread `json:"thread,omitempty" jsonschema:"description=Thread context if this message is part of a thread"`
	Embeds    []DiscordEmbed `json:"embeds,omitempty" jsonschema:"description=Rich embeds attached to the message"`
	Mentions  []string       `json:"mentions,omitempty" jsonschema:"description=Usernames mentioned in the message"`
	Timestamp string         `json:"ts" jsonschema:"description=ISO 8601 timestamp,format=date-time"`
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

// newDiscordEvent builds a DiscordEventData payload from the minimal fields
// the bot callback and the /inject endpoint provide.
func newDiscordEvent(guildID, channelID, sender, content string, ts time.Time) DiscordEventData {
	log.Printf("[discord] guild=%s channel=%s sender=%s text=%q", guildID, channelID, sender, content)
	return DiscordEventData{
		GuildID:   guildID,
		ChannelID: channelID,
		Author:    DiscordAuthor{Username: sender},
		Content:   content,
		Type:      "default",
		Timestamp: ts.Format(time.RFC3339),
	}
}
