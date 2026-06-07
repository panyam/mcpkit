package main

import (
	"encoding/json"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
	"github.com/panyam/mcpkit/server"
)

const recentLimit = 50

// registerResources mounts MCP resources that surface recent payloads
// for the cursored sources. presence.changed has no resource — its
// cursorless nature means there's nothing to "read back," only live
// transitions.
func registerResources(srv *server.Server,
	chatSrc *events.YieldingSource[ChatMessageData],
	alertSrc *events.YieldingSource[AlertData],
) {
	srv.RegisterResource(
		core.ResourceDef{
			URI:         "chat://messages/recent",
			Name:        "Recent Chat Messages",
			Description: "The most recent chat message payloads.",
			MimeType:    "application/json",
		},
		func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			data, _ := json.Marshal(chatSrc.Recent(recentLimit))
			return core.ResourceResult{
				Contents: []core.ResourceReadContent{{
					URI: req.URI, MimeType: "application/json", Text: string(data),
				}},
			}, nil
		},
	)
	srv.RegisterResource(
		core.ResourceDef{
			URI:         "alerts://alerts/recent",
			Name:        "Recent Alerts",
			Description: "The most recent alert payloads (unredacted; subscribers see redacted variants if they opt in).",
			MimeType:    "application/json",
		},
		func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			data, _ := json.Marshal(alertSrc.Recent(recentLimit))
			return core.ResourceResult{
				Contents: []core.ResourceReadContent{{
					URI: req.URI, MimeType: "application/json", Text: string(data),
				}},
			}, nil
		},
	)
}
