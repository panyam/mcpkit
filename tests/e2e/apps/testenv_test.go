// Package apps_test contains conformance tests for the MCP Apps extension
// (io.modelcontextprotocol/ui). These validate server-side metadata correctness,
// capability negotiation, visibility filtering, and resource serving.
//
// Run via "make test-e2e" from the project root, or
// "cd tests/e2e && go test ./apps/ -v" directly.
package apps_test

import (
	"context"
	"net/http/httptest"
	"testing"

	client "github.com/panyam/mcpkit/client"
	core "github.com/panyam/mcpkit/core"
	ui "github.com/panyam/mcpkit/ext/ui"
	server "github.com/panyam/mcpkit/server"
)

// newConformanceServer creates the standard MCP Apps conformance server with
// UI tools and resources matching cmd/testserver/conformance_apps.go. This
// allows the test suite to run without starting the external test server.
func newConformanceServer() *server.Server {
	srv := server.NewServer(
		core.ServerInfo{Name: "apps-conformance", Version: "0.1.0"},
		server.WithExtension(ui.UIExtension{}),
		server.WithSubscriptions(),
	)

	border := false

	// show-dashboard: full UI metadata
	srv.RegisterTool(
		core.ToolDef{
			Name:        "show-dashboard",
			Description: "Shows the dashboard UI",
			InputSchema: map[string]any{"type": "object"},
			Meta: &core.ToolMeta{
				UI: &core.UIMetadata{
					ResourceUri: "ui://dashboard/view",
					Visibility:  []core.UIVisibility{core.UIVisibilityModel, core.UIVisibilityApp},
					CSP: &core.UICSPConfig{
						ResourceDomains: []string{"cdn.example.com"},
					},
					Permissions:   []string{"clipboard-write"},
					PrefersBorder: &border,
				},
			},
		},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("Dashboard displayed"), nil
		},
	)

	// navigate-dashboard: app-only (not visible to model)
	srv.RegisterTool(
		core.ToolDef{
			Name:        "navigate-dashboard",
			Description: "Navigates within the dashboard (app-only)",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"page": map[string]any{"type": "string"}},
			},
			Meta: &core.ToolMeta{
				UI: &core.UIMetadata{
					Visibility: []core.UIVisibility{core.UIVisibilityApp},
				},
			},
		},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			var args struct{ Page string `json:"page"` }
			req.Bind(&args)
			return core.TextResult("Navigated to " + args.Page), nil
		},
	)

	// dashboard-data: model+app, same resourceUri
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
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult(`Dashboard data: {"widgets": 5}`), nil
		},
	)

	// mutate-dashboard: sends resource change notification
	srv.RegisterTool(
		core.ToolDef{
			Name:        "mutate-dashboard",
			Description: "Mutates dashboard state and notifies",
			InputSchema: map[string]any{"type": "object"},
			Meta: &core.ToolMeta{
				UI: &core.UIMetadata{
					ResourceUri: "ui://dashboard/view",
					Visibility:  []core.UIVisibility{core.UIVisibilityModel, core.UIVisibilityApp},
				},
			},
		},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			core.NotifyResourcesChanged(ctx)
			return core.TextResult("Dashboard mutated"), nil
		},
	)

	// plain-tool: no UI metadata at all
	srv.RegisterTool(
		core.ToolDef{
			Name:        "plain-tool",
			Description: "Tool without UI metadata",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("plain result"), nil
		},
	)

	// ui://dashboard/view — HTML resource with per-content _meta
	srv.RegisterResource(
		core.ResourceDef{URI: "ui://dashboard/view", Name: "Dashboard View", MimeType: core.AppMIMEType},
		func(ctx context.Context, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{Contents: []core.ResourceReadContent{{
				URI:      req.URI,
				MimeType: core.AppMIMEType,
				Text:     `<!DOCTYPE html><html><body><h1>Dashboard</h1></body></html>`,
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
		core.ResourceTemplate{URITemplate: "ui://apps/{id}/view", Name: "App View", MimeType: core.AppMIMEType},
		func(ctx context.Context, uri string, params map[string]string) (core.ResourceResult, error) {
			return core.ResourceResult{Contents: []core.ResourceReadContent{{
				URI:      uri,
				MimeType: core.AppMIMEType,
				Text:     `<!DOCTYPE html><html><body><h1>App ` + params["id"] + `</h1></body></html>`,
			}}}, nil
		},
	)

	// test://plain-resource — non-UI resource for comparison
	srv.RegisterResource(
		core.ResourceDef{URI: "test://plain-resource", Name: "Plain Resource", MimeType: "text/plain"},
		func(ctx context.Context, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{Contents: []core.ResourceReadContent{{
				URI: req.URI, MimeType: "text/plain", Text: "plain content",
			}}}, nil
		},
	)

	return srv
}

// setupConformanceClient creates an httptest.Server with the conformance server
// and a connected client with WithUIExtension.
func setupConformanceClient(t *testing.T) *client.Client {
	t.Helper()
	srv := newConformanceServer()
	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "apps-conformance-client", Version: "1.0"},
		client.WithUIExtension(),
	)
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// setupConformanceClientWithNotify creates a conformance client with a custom
// notification callback for testing server-to-client notifications.
func setupConformanceClientWithNotify(t *testing.T, onNotify func(method string, params any)) *client.Client {
	t.Helper()
	srv := newConformanceServer()
	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "apps-conformance-client", Version: "1.0"},
		client.WithUIExtension(),
		client.WithNotificationCallback(onNotify),
	)
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}
