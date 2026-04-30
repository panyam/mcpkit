package main

import (
	"encoding/json"
	"fmt"

	"github.com/bwmarrin/discordgo"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
	"github.com/panyam/mcpkit/server"
)

const recentLimit = 50

// registerResources registers MCP resources that read typed payloads
// directly from the YieldingSource — no separate buffer, no unmarshal cycle.
func registerResources(srv *server.Server, source *events.YieldingSource[DiscordEventData]) {
	srv.RegisterResource(
		core.ResourceDef{
			URI:         "discord://messages/recent",
			Name:        "Recent Discord Messages",
			Description: "The most recent Discord message payloads received by the bot.",
			MimeType:    "application/json",
		},
		func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			payloads := source.Recent(recentLimit)
			data, _ := json.Marshal(payloads)
			return core.ResourceResult{
				Contents: []core.ResourceReadContent{{
					URI: req.URI, MimeType: "application/json", Text: string(data),
				}},
			}, nil
		},
	)

	srv.RegisterResourceTemplate(
		core.ResourceTemplate{
			URITemplate: "discord://message/{cursor}",
			Name:        "Discord Message",
			Description: "A single Discord message identified by its event cursor.",
			MimeType:    "application/json",
		},
		func(ctx core.ResourceContext, uri string, params map[string]string) (core.ResourceResult, error) {
			cursor := params["cursor"]
			payload, ok := source.ByCursor(cursor)
			if !ok {
				return core.ResourceResult{}, fmt.Errorf("message with cursor %q not found", cursor)
			}
			data, _ := json.Marshal(payload)
			return core.ResourceResult{
				Contents: []core.ResourceReadContent{{
					URI: uri, MimeType: "application/json", Text: string(data),
				}},
			}, nil
		},
	)
}

type sendMessageInput struct {
	ChannelID string `json:"channel_id" jsonschema:"description=Discord channel ID to send the message to.,required"`
	Text      string `json:"text" jsonschema:"description=Message text to send.,required"`
}

func registerTools(srv *server.Server, session *discordgo.Session) {
	srv.Register(core.TextTool[sendMessageInput](
		"send_message",
		"Send a text message to a Discord channel.",
		func(ctx core.ToolContext, input sendMessageInput) (string, error) {
			if session == nil {
				return "", fmt.Errorf("discord bot not configured (running in test mode)")
			}
			msg, err := session.ChannelMessageSend(input.ChannelID, input.Text)
			if err != nil {
				return "", fmt.Errorf("discord send failed: %w", err)
			}
			return fmt.Sprintf("sent (id: %s)", msg.ID), nil
		},
	))
}
