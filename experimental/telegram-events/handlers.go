package main

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// registerResources registers MCP resources for reading Telegram messages.
// Delivery-mode tools/methods are registered separately via EventDelivery.
func registerResources(srv *server.Server, store *MessageStore) {
	// Static resource: recent messages
	srv.RegisterResource(
		core.ResourceDef{
			URI:         "telegram://messages/recent",
			Name:        "Recent Telegram Messages",
			Description: "The most recent 50 Telegram messages received by the bot.",
			MimeType:    "application/json",
		},
		func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			msgs := store.Recent(50)
			data, err := json.Marshal(msgs)
			if err != nil {
				return core.ResourceResult{}, fmt.Errorf("marshal messages: %w", err)
			}
			return core.ResourceResult{
				Contents: []core.ResourceReadContent{{
					URI:      req.URI,
					MimeType: "application/json",
					Text:     string(data),
				}},
			}, nil
		},
	)

	// Template resource: single message by ID
	srv.RegisterResourceTemplate(
		core.ResourceTemplate{
			URITemplate: "telegram://message/{id}",
			Name:        "Telegram Message",
			Description: "A single Telegram message by its ID.",
			MimeType:    "application/json",
		},
		func(ctx core.ResourceContext, uri string, params map[string]string) (core.ResourceResult, error) {
			idStr := params["id"]
			id, err := strconv.ParseInt(idStr, 10, 64)
			if err != nil {
				return core.ResourceResult{}, fmt.Errorf("invalid message ID %q: %w", idStr, err)
			}
			msg := store.GetByID(id)
			if msg == nil {
				return core.ResourceResult{}, fmt.Errorf("message %d not found", id)
			}
			data, err := json.Marshal(msg)
			if err != nil {
				return core.ResourceResult{}, fmt.Errorf("marshal message: %w", err)
			}
			return core.ResourceResult{
				Contents: []core.ResourceReadContent{{
					URI:      uri,
					MimeType: "application/json",
					Text:     string(data),
				}},
			}, nil
		},
	)
}
