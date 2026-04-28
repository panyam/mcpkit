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
	srv.Register(core.TextTool[struct{}]("show-dashboard", "Shows the dashboard UI",
		func(ctx core.ToolContext, _ struct{}) (string, error) {
			return "Dashboard displayed", nil
		},
		core.WithToolMeta(&core.ToolMeta{
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
		}),
	))

	// navigate-dashboard: app-only tool (not visible to model)
	type navigateInput struct {
		Page string `json:"page,omitempty"`
	}
	srv.Register(core.TextTool[navigateInput]("navigate-dashboard", "Navigates within the dashboard (app-only)",
		func(ctx core.ToolContext, input navigateInput) (string, error) {
			return fmt.Sprintf("Navigated to %s", input.Page), nil
		},
		core.WithToolMeta(&core.ToolMeta{
			UI: &core.UIMetadata{
				Visibility: []core.UIVisibility{core.UIVisibilityApp},
			},
		}),
	))

	// dashboard-data: model+app tool sharing the same resourceUri as show-dashboard
	srv.Register(core.TextTool[struct{}]("dashboard-data", "Returns dashboard data",
		func(ctx core.ToolContext, _ struct{}) (string, error) {
			return "Dashboard data: {\"widgets\": 5}", nil
		},
		core.WithToolMeta(&core.ToolMeta{
			UI: &core.UIMetadata{
				ResourceUri: "ui://dashboard/view",
				Visibility:  []core.UIVisibility{core.UIVisibilityModel, core.UIVisibilityApp},
			},
		}),
	))

	// mutate-dashboard: mutates state and sends resource change notification
	srv.Register(core.TextTool[struct{}]("mutate-dashboard", "Mutates dashboard state and notifies resource change",
		func(ctx core.ToolContext, _ struct{}) (string, error) {
			ctx.NotifyResourcesChanged()
			return "Dashboard mutated", nil
		},
		core.WithToolMeta(&core.ToolMeta{
			UI: &core.UIMetadata{
				ResourceUri: "ui://dashboard/view",
				Visibility:  []core.UIVisibility{core.UIVisibilityModel, core.UIVisibilityApp},
			},
		}),
	))

	// request-fullscreen: requests display mode change via notification
	srv.Register(core.TextTool[struct{}]("request-fullscreen", "Requests fullscreen display mode",
		func(ctx core.ToolContext, _ struct{}) (string, error) {
			// Uses raw ctx.Notify instead of ui.RequestDisplayMode to avoid
			// importing ext/ui from the root module (see testUIExtension above).
			ctx.Notify("notifications/ui/displayMode", map[string]any{
				"displayMode": core.DisplayModeFullscreen,
			})
			return "Fullscreen requested", nil
		},
		core.WithToolMeta(&core.ToolMeta{
			UI: &core.UIMetadata{
				ResourceUri:           "ui://dashboard/view",
				Visibility:            []core.UIVisibility{core.UIVisibilityApp},
				SupportedDisplayModes: []core.DisplayMode{core.DisplayModeInline, core.DisplayModeFullscreen},
			},
		}),
	))

	// elicit-with-ui: demonstrates app-backed elicitation with _meta.ui
	srv.Register(core.TypedTool[struct{}, core.ToolResult]("elicit-with-ui", "Elicits input using an MCP App UI",
		func(ctx core.ToolContext, _ struct{}) (core.ToolResult, error) {
			result, err := ctx.Elicit(core.ElicitationRequest{
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
		core.WithToolMeta(&core.ToolMeta{
			UI: &core.UIMetadata{
				ResourceUri: "ui://dashboard/view",
				Visibility:  []core.UIVisibility{core.UIVisibilityModel, core.UIVisibilityApp},
			},
		}),
	))

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

	// test-app-tools: tool whose resource HTML registers app-provided tools.
	// Exercises the registerTool() bridge API. A host that supports app-provided
	// tools can call tools/list and tools/call on this app's iframe.
	srv.Register(core.TextTool[struct{}]("test-app-tools", "Opens an app that registers its own tools",
		func(ctx core.ToolContext, _ struct{}) (string, error) {
			return "App with registered tools displayed", nil
		},
		core.WithToolMeta(&core.ToolMeta{
			UI: &core.UIMetadata{
				ResourceUri: "ui://app-tools/view",
				Visibility:  []core.UIVisibility{core.UIVisibilityModel, core.UIVisibilityApp},
			},
		}),
	))

	// ui://app-tools/view — HTML that uses registerTool() to provide app tools.
	// A conformant host can call tools/list on this iframe and get back the
	// registered tools, then call tools/call to invoke them.
	srv.RegisterResource(
		core.ResourceDef{
			URI:      "ui://app-tools/view",
			Name:     "App Tools View",
			MimeType: core.AppMIMEType,
		},
		func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			html := `<!DOCTYPE html>
<html><head><title>App Tools Test</title></head>
<body>
<h1>App-Provided Tools</h1>
<p id="status">Registering tools...</p>
<script>
// Register app-provided tools for conformance testing.
if (typeof MCPApp !== 'undefined' && MCPApp.registerTool) {
  var counter = 0;
  MCPApp.registerTool('app-counter', {
    description: 'Increment and return a counter value',
    inputSchema: { type: 'object', properties: { amount: { type: 'number' } } }
  }, function(args) {
    counter += (args.amount || 1);
    return { content: [{ type: 'text', text: 'counter=' + counter }] };
  });
  MCPApp.registerTool('app-echo', {
    description: 'Echo back the input arguments',
    inputSchema: { type: 'object', properties: { message: { type: 'string' } } }
  }, function(args) {
    return { content: [{ type: 'text', text: 'echo: ' + (args.message || '') }] };
  });
  document.getElementById('status').textContent = 'Tools registered: app-counter, app-echo';
}
</script>
</body></html>`
			return core.ResourceResult{Contents: []core.ResourceReadContent{{
				URI:      req.URI,
				MimeType: core.AppMIMEType,
				Text:     html,
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
