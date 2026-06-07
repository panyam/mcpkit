package main

import (
	"encoding/json"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
	"github.com/panyam/mcpkit/server"
)

const recentLimit = 50

// registerResources mounts the MCP resource that surfaces recent chat
// payloads. The resource reads directly from the HTTPSource's typed
// buffer — no separate state, no unmarshal cycle. Same pattern as the
// discord example's recent-messages resource.
func registerResources(srv *server.Server, chat *events.HTTPSource[ChatMessageData]) {
	srv.RegisterResource(
		core.ResourceDef{
			URI:         "chat://messages/recent",
			Name:        "Recent Chat Messages",
			Description: "The most recent chat message payloads received by the event-server.",
			MimeType:    "application/json",
		},
		func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			payloads := chat.Recent(recentLimit)
			data, _ := json.Marshal(payloads)
			return core.ResourceResult{
				Contents: []core.ResourceReadContent{{
					URI:      req.URI,
					MimeType: "application/json",
					Text:     string(data),
				}},
			}, nil
		},
	)
}
