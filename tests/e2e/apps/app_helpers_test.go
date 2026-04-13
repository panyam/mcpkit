package apps_test

import (
	"net/http/httptest"
	"testing"

	client "github.com/panyam/mcpkit/client"
	core "github.com/panyam/mcpkit/core"
	ui "github.com/panyam/mcpkit/ext/ui"
	server "github.com/panyam/mcpkit/server"
)

// TestRegisterAppToolE2E verifies that RegisterAppTool correctly registers both
// a tool with _meta.ui and a matching resource, end-to-end over the wire.
// The client calls tools/list and resources/read to verify the tool metadata
// and HTML content survive the full server → HTTP → client path.
func TestRegisterAppToolE2E(t *testing.T) {
	srv := server.NewServer(
		core.ServerInfo{Name: "app-helpers-e2e", Version: "0.1.0"},
		server.WithExtension(ui.UIExtension{}),
	)

	ui.RegisterAppTool(srv, ui.AppToolConfig{
		Name:        "build_deck",
		Description: "Build a slide deck",
		InputSchema: map[string]any{"type": "object"},
		ResourceURI: "ui://decks/view",
		ToolHandler: func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("deck built"), nil
		},
		ResourceHandler: func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{Contents: []core.ResourceReadContent{{
				URI:      req.URI,
				MimeType: core.AppMIMEType,
				Text:     "<html><body>deck viewer</body></html>",
			}}}, nil
		},
		Visibility: []core.UIVisibility{core.UIVisibilityModel, core.UIVisibilityApp},
	})

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"},
		client.WithUIExtension(),
	)
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	// Verify tool has _meta.ui
	tools, err := c.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var deckTool *core.ToolDef
	for i := range tools {
		if tools[i].Name == "build_deck" {
			deckTool = &tools[i]
			break
		}
	}
	if deckTool == nil {
		t.Fatal("build_deck tool not found")
	}
	if deckTool.Meta == nil || deckTool.Meta.UI == nil {
		t.Fatal("build_deck: _meta.ui is nil")
	}
	if deckTool.Meta.UI.ResourceUri != "ui://decks/view" {
		t.Errorf("resourceUri = %q", deckTool.Meta.UI.ResourceUri)
	}

	// Verify resource serves HTML
	result, err := c.Call("resources/read", map[string]string{"uri": "ui://decks/view"})
	if err != nil {
		t.Fatalf("resources/read: %v", err)
	}
	var resp core.ResourceResult
	result.Unmarshal(&resp)
	if len(resp.Contents) != 1 {
		t.Fatalf("got %d contents, want 1", len(resp.Contents))
	}
	if resp.Contents[0].MimeType != core.AppMIMEType {
		t.Errorf("mimeType = %q, want %q", resp.Contents[0].MimeType, core.AppMIMEType)
	}

	// Verify tool call works
	text, err := c.ToolCall("build_deck", nil)
	if err != nil {
		t.Fatalf("ToolCall: %v", err)
	}
	if text != "deck built" {
		t.Errorf("result = %q", text)
	}
}

// TestNotifyResourcesChangedE2E verifies that NotifyResourcesChanged sends
// a notifications/resources/list_changed notification through the wire.
func TestNotifyResourcesChangedE2E(t *testing.T) {
	srv := server.NewServer(
		core.ServerInfo{Name: "notify-e2e", Version: "0.1.0"},
	)

	srv.RegisterTool(
		core.ToolDef{
			Name:        "mutate",
			Description: "Mutates state and notifies",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			core.NotifyResourcesChanged(ctx)
			return core.TextResult("mutated"), nil
		},
	)

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	var notifMethod string
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"},
		client.WithNotificationCallback(func(method string, params any) {
			notifMethod = method
		}),
	)
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	_, err := c.ToolCall("mutate", nil)
	if err != nil {
		t.Fatalf("ToolCall: %v", err)
	}

	if notifMethod != "notifications/resources/list_changed" {
		t.Errorf("notification method = %q, want %q", notifMethod, "notifications/resources/list_changed")
	}
}
