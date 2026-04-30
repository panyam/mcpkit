package main

import (
	"encoding/json"
	"fmt"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
	"github.com/panyam/mcpkit/server"
)

const recentLimit = 50

// registerResources registers MCP resources that read typed payloads
// directly from the YieldingSource — no separate buffer, no unmarshal cycle.
func registerResources(srv *server.Server, source *events.YieldingSource[TelegramEventData]) {
	srv.RegisterResource(
		core.ResourceDef{
			URI:         "telegram://messages/recent",
			Name:        "Recent Telegram Messages",
			Description: "The most recent Telegram message payloads received by the bot.",
			MimeType:    "application/json",
		},
		func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			payloads := source.Recent(recentLimit)
			data, err := json.Marshal(payloads)
			if err != nil {
				return core.ResourceResult{}, fmt.Errorf("marshal messages: %w", err)
			}
			return core.ResourceResult{
				Contents: []core.ResourceReadContent{{
					URI: req.URI, MimeType: "application/json", Text: string(data),
				}},
			}, nil
		},
	)

	srv.RegisterResourceTemplate(
		core.ResourceTemplate{
			URITemplate: "telegram://message/{cursor}",
			Name:        "Telegram Message",
			Description: "A single Telegram message identified by its event cursor.",
			MimeType:    "application/json",
		},
		func(ctx core.ResourceContext, uri string, params map[string]string) (core.ResourceResult, error) {
			cursor := params["cursor"]
			payload, ok := source.ByCursor(cursor)
			if !ok {
				return core.ResourceResult{}, fmt.Errorf("message with cursor %q not found", cursor)
			}
			data, err := json.Marshal(payload)
			if err != nil {
				return core.ResourceResult{}, fmt.Errorf("marshal message: %w", err)
			}
			return core.ResourceResult{
				Contents: []core.ResourceReadContent{{
					URI: uri, MimeType: "application/json", Text: string(data),
				}},
			}, nil
		},
	)
}
