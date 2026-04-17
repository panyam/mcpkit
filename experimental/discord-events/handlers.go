package main

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/bwmarrin/discordgo"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// registerResources registers MCP resources for reading Discord messages.
func registerResources(srv *server.Server, store *MessageStore) {
	srv.RegisterResource(
		core.ResourceDef{
			URI:         "discord://messages/recent",
			Name:        "Recent Discord Messages",
			Description: "The most recent 50 Discord messages received by the bot.",
			MimeType:    "application/json",
		},
		func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			msgs := store.Recent(50)
			data, _ := json.Marshal(msgs)
			return core.ResourceResult{
				Contents: []core.ResourceReadContent{{
					URI: req.URI, MimeType: "application/json", Text: string(data),
				}},
			}, nil
		},
	)

	srv.RegisterResourceTemplate(
		core.ResourceTemplate{
			URITemplate: "discord://message/{id}",
			Name:        "Discord Message",
			Description: "A single Discord message by its ID.",
			MimeType:    "application/json",
		},
		func(ctx core.ResourceContext, uri string, params map[string]string) (core.ResourceResult, error) {
			id, err := strconv.ParseInt(params["id"], 10, 64)
			if err != nil {
				return core.ResourceResult{}, fmt.Errorf("invalid message ID %q: %w", params["id"], err)
			}
			msg := store.GetByID(id)
			if msg == nil {
				return core.ResourceResult{}, fmt.Errorf("message %d not found", id)
			}
			data, _ := json.Marshal(msg)
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

// registerTools registers the send_message tool.
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
