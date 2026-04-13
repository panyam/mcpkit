package main

// Conformance test tools and resources for the MCP Apps extension
// (io.modelcontextprotocol/ui). These exercise metadata, visibility,
// resource serving, and resource change notifications.

import (
	"fmt"

	core "github.com/panyam/mcpkit/core"
	server "github.com/panyam/mcpkit/server"
)

// testUIExtension declares the MCP Apps extension for the test server.
// Inlined here to avoid the root module depending on ext/ui.
type testUIExtension struct{}

func (testUIExtension) Extension() core.Extension {
	return core.Extension{
		ID:          core.UIExtensionID,
		SpecVersion: "2026-01-26",
		Stability:   core.Experimental,
	}
}

// registerConformanceApps adds UI tools and resources to the test server
// for MCP Apps conformance testing.
func registerConformanceApps(srv *server.Server) {
	border := false

	// show-dashboard: tool with full UI metadata
	srv.RegisterTool(
		core.ToolDef{
			Name:        "show-dashboard",
			Description: "Shows the dashboard UI",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
			Meta: &core.ToolMeta{
				UI: &core.UIMetadata{
					ResourceUri: "ui://dashboard/view",
					Visibility:  []core.UIVisibility{core.UIVisibilityModel, core.UIVisibilityApp},
					CSP: &core.UICSPConfig{
						ResourceDomains: []string{"cdn.example.com"},
					},
					Permissions:           []string{"clipboard-write"},
					PrefersBorder:         &border,
					SupportedDisplayModes: []core.DisplayMode{core.DisplayModeInline, core.DisplayModeFullscreen},
				},
			},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("Dashboard displayed"), nil
		},
	)

	// navigate-dashboard: app-only tool (not visible to model)
	srv.RegisterTool(
		core.ToolDef{
			Name:        "navigate-dashboard",
			Description: "Navigates within the dashboard (app-only)",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"page": map[string]any{"type": "string"},
				},
			},
			Meta: &core.ToolMeta{
				UI: &core.UIMetadata{
					Visibility: []core.UIVisibility{core.UIVisibilityApp},
				},
			},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			var args struct {
				Page string `json:"page"`
			}
			req.Bind(&args)
			return core.TextResult(fmt.Sprintf("Navigated to %s", args.Page)), nil
		},
	)

	// dashboard-data: model+app tool sharing the same resourceUri as show-dashboard
	srv.RegisterTool(
		core.ToolDef{
			Name:        "dashboard-data",
			Description: "Returns dashboard data",
			InputSchema: map[string]any{"type": "object"},
			Meta: &core.ToolMeta{
				UI: &core.UIMetadata{
					ResourceUri: "ui://dashboard/view",
					Visibility:  []core.UIVisibility{core.UIVisibilityModel, core.UIVisibilityApp},
				},
			},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("Dashboard data: {\"widgets\": 5}"), nil
		},
	)

	// mutate-dashboard: mutates state and sends resource change notification
	srv.RegisterTool(
		core.ToolDef{
			Name:        "mutate-dashboard",
			Description: "Mutates dashboard state and notifies resource change",
			InputSchema: map[string]any{"type": "object"},
			Meta: &core.ToolMeta{
				UI: &core.UIMetadata{
					ResourceUri: "ui://dashboard/view",
					Visibility:  []core.UIVisibility{core.UIVisibilityModel, core.UIVisibilityApp},
				},
			},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			core.NotifyResourcesChanged(ctx)
			return core.TextResult("Dashboard mutated"), nil
		},
	)

	// request-fullscreen: requests display mode change via notification
	srv.RegisterTool(
		core.ToolDef{
			Name:        "request-fullscreen",
			Description: "Requests fullscreen display mode",
			InputSchema: map[string]any{"type": "object"},
			Meta: &core.ToolMeta{
				UI: &core.UIMetadata{
					ResourceUri:           "ui://dashboard/view",
					Visibility:            []core.UIVisibility{core.UIVisibilityApp},
					SupportedDisplayModes: []core.DisplayMode{core.DisplayModeInline, core.DisplayModeFullscreen},
				},
			},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			// Uses raw core.Notify instead of ui.RequestDisplayMode to avoid
			// importing ext/ui from the root module (see testUIExtension above).
			core.Notify(ctx, "notifications/ui/displayMode", map[string]any{
				"displayMode": core.DisplayModeFullscreen,
			})
			return core.TextResult("Fullscreen requested"), nil
		},
	)

	// elicit-with-ui: demonstrates app-backed elicitation with _meta.ui
	srv.RegisterTool(
		core.ToolDef{
			Name:        "elicit-with-ui",
			Description: "Elicits input using an MCP App UI",
			InputSchema: map[string]any{"type": "object"},
			Meta: &core.ToolMeta{
				UI: &core.UIMetadata{
					ResourceUri: "ui://dashboard/view",
					Visibility:  []core.UIVisibility{core.UIVisibilityModel, core.UIVisibilityApp},
				},
			},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			result, err := core.Elicit(ctx, core.ElicitationRequest{
				Message: "Choose a dashboard widget",
				Meta: &core.ElicitationMeta{
					UI: &core.UIMetadata{
						ResourceUri: "ui://dashboard/view",
					},
				},
			})
			if err != nil {
				return core.ErrorResult(err.Error()), nil
			}
			return core.TextResult(fmt.Sprintf("Action: %s", result.Action)), nil
		},
	)

	// ui://dashboard/view — static HTML resource for the dashboard
	srv.RegisterResource(
		core.ResourceDef{
			URI:      "ui://dashboard/view",
			Name:     "Dashboard View",
			MimeType: core.AppMIMEType,
		},
		func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{Contents: []core.ResourceReadContent{{
				URI:      req.URI,
				MimeType: core.AppMIMEType,
				Text:     `<!DOCTYPE html><html><head><title>Dashboard</title></head><body><h1>Dashboard</h1></body></html>`,
				Meta: &core.ResourceContentMeta{
					UI: &core.UIMetadata{
						ResourceUri: "ui://dashboard/view",
						Permissions: []string{"clipboard-write"},
					},
				},
			}}}, nil
		},
	)

	// ui://apps/{id}/view — parameterized template resource
	srv.RegisterResourceTemplate(
		core.ResourceTemplate{
			URITemplate: "ui://apps/{id}/view",
			Name:        "App View",
			MimeType:    core.AppMIMEType,
		},
		func(ctx core.ResourceContext, uri string, params map[string]string) (core.ResourceResult, error) {
			return core.ResourceResult{Contents: []core.ResourceReadContent{{
				URI:      uri,
				MimeType: core.AppMIMEType,
				Text:     fmt.Sprintf(`<!DOCTYPE html><html><body><h1>App %s</h1></body></html>`, params["id"]),
			}}}, nil
		},
	)
}
