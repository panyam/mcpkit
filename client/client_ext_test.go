package client_test

import (
	"context"
	"net/http/httptest"
	"testing"

	client "github.com/panyam/mcpkit/client"
	core "github.com/panyam/mcpkit/core"
	server "github.com/panyam/mcpkit/server"
)

// testUIExtension is a minimal ExtensionProvider for testing without importing ext/ui.
type testUIExtension struct{}

func (testUIExtension) Extension() core.Extension {
	return core.Extension{
		ID:          core.UIExtensionID,
		SpecVersion: "2026-01-26",
		Stability:   core.Experimental,
	}
}

// newUITestServer creates a server with UIExtension registered and tools with
// various visibility settings for testing ListToolsForModel filtering.
func newUITestServer() *server.Server {
	srv := server.NewServer(
		core.ServerInfo{Name: "ui-test-server", Version: "1.0.0"},
		server.WithExtension(testUIExtension{}),
	)

	// Tool visible to both model and app (default — no visibility set)
	srv.RegisterTool(
		core.ToolDef{
			Name:        "public_tool",
			Description: "Visible to everyone",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("public"), nil
		},
	)

	// Tool explicitly visible to model
	srv.RegisterTool(
		core.ToolDef{
			Name:        "model_tool",
			Description: "Visible to model",
			InputSchema: map[string]any{"type": "object"},
			Meta: &core.ToolMeta{
				UI: &core.UIMetadata{
					Visibility: []core.UIVisibility{core.UIVisibilityModel},
				},
			},
		},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("model"), nil
		},
	)

	// Tool visible to both model and app
	srv.RegisterTool(
		core.ToolDef{
			Name:        "both_tool",
			Description: "Visible to model and app",
			InputSchema: map[string]any{"type": "object"},
			Meta: &core.ToolMeta{
				UI: &core.UIMetadata{
					Visibility:  []core.UIVisibility{core.UIVisibilityModel, core.UIVisibilityApp},
					ResourceUri: "ui://test/view",
				},
			},
		},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("both"), nil
		},
	)

	// Tool visible ONLY to apps (should be filtered out by ListToolsForModel)
	srv.RegisterTool(
		core.ToolDef{
			Name:        "app_only_tool",
			Description: "Only for apps",
			InputSchema: map[string]any{"type": "object"},
			Meta: &core.ToolMeta{
				UI: &core.UIMetadata{
					Visibility: []core.UIVisibility{core.UIVisibilityApp},
				},
			},
		},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("app-only"), nil
		},
	)

	// Tool that reports whether client supports UI (for negotiation test)
	srv.RegisterTool(
		core.ToolDef{
			Name:        "check_ui",
			Description: "Reports client UI support",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			if core.ClientSupportsUI(ctx) {
				return core.TextResult("ui: yes"), nil
			}
			return core.TextResult("ui: no"), nil
		},
	)

	return srv
}

// setupUIStreamableClient creates an httptest.Server with UIExtension and a
// connected Client configured with WithUIExtension.
func setupUIStreamableClient(t *testing.T, opts ...client.ClientOption) (*client.Client, *httptest.Server) {
	t.Helper()
	srv := newUITestServer()
	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	allOpts := append([]client.ClientOption{client.WithUIExtension()}, opts...)
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "ui-test-client", Version: "1.0"}, allOpts...)
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	return c, ts
}

// TestClientWithUIExtension verifies that a client configured with
// WithUIExtension() advertises UI support during initialize, which the server
// can detect via core.ClientSupportsUI(ctx) inside a tool handler. This
// validates the client→server direction of extension negotiation.
func TestClientWithUIExtension(t *testing.T) {
	c, _ := setupUIStreamableClient(t)

	text, err := c.ToolCall("check_ui", nil)
	if err != nil {
		t.Fatalf("ToolCall: %v", err)
	}
	if text != "ui: yes" {
		t.Errorf("result = %q, want 'ui: yes'", text)
	}
}

// TestClientWithoutUIExtension verifies that a client without WithUIExtension()
// does NOT advertise UI support, so the server reports no UI capability.
func TestClientWithoutUIExtension(t *testing.T) {
	srv := newUITestServer()
	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "plain-client", Version: "1.0"})
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}

	text, err := c.ToolCall("check_ui", nil)
	if err != nil {
		t.Fatalf("ToolCall: %v", err)
	}
	if text != "ui: no" {
		t.Errorf("result = %q, want 'ui: no'", text)
	}
}

// TestClientServerSupportsUI verifies that ServerSupportsUI() returns true
// when the server has UIExtension registered, and false when it doesn't.
// This validates the server→client direction of extension negotiation.
func TestClientServerSupportsUI(t *testing.T) {
	t.Run("server with UI", func(t *testing.T) {
		c, _ := setupUIStreamableClient(t)
		if !c.ServerSupportsUI() {
			t.Error("ServerSupportsUI() should be true")
		}
	})

	t.Run("server without UI", func(t *testing.T) {
		srv := server.NewServer(core.ServerInfo{Name: "plain", Version: "1.0"})
		srv.RegisterTool(
			core.ToolDef{Name: "noop", Description: "noop", InputSchema: map[string]any{"type": "object"}},
			func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
				return core.TextResult("ok"), nil
			},
		)
		handler := srv.Handler(server.WithStreamableHTTP(true))
		ts := httptest.NewServer(handler)
		t.Cleanup(ts.Close)

		c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"})
		if err := c.Connect(); err != nil {
			t.Fatalf("Connect failed: %v", err)
		}
		if c.ServerSupportsUI() {
			t.Error("ServerSupportsUI() should be false")
		}
	})
}

// TestClientServerSupportsExtension verifies the general ServerSupportsExtension
// method works for any extension ID, not just UI.
func TestClientServerSupportsExtension(t *testing.T) {
	c, _ := setupUIStreamableClient(t)

	if !c.ServerSupportsExtension(core.UIExtensionID) {
		t.Error("should support UI extension")
	}
	if c.ServerSupportsExtension("io.example/nonexistent") {
		t.Error("should not support unknown extension")
	}
}

// TestClientListToolsForModel verifies that ListToolsForModel() filters tools
// based on visibility metadata. Tools with no visibility (default), visibility
// including "model", or visibility ["model", "app"] are included. Tools with
// visibility ["app"] only are excluded. ListTools() still returns all tools.
func TestClientListToolsForModel(t *testing.T) {
	c, _ := setupUIStreamableClient(t)

	// ListTools returns ALL tools (unfiltered)
	allTools, err := c.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(allTools) != 5 {
		t.Fatalf("ListTools: got %d tools, want 5", len(allTools))
	}

	// ListToolsForModel excludes app-only tools
	modelTools, err := c.ListToolsForModel()
	if err != nil {
		t.Fatalf("ListToolsForModel: %v", err)
	}

	names := make(map[string]bool)
	for _, tool := range modelTools {
		names[tool.Name] = true
	}

	// These should be included
	for _, want := range []string{"public_tool", "model_tool", "both_tool", "check_ui"} {
		if !names[want] {
			t.Errorf("ListToolsForModel should include %q", want)
		}
	}

	// This should be excluded
	if names["app_only_tool"] {
		t.Error("ListToolsForModel should NOT include app_only_tool")
	}

	if len(modelTools) != 4 {
		t.Errorf("ListToolsForModel: got %d tools, want 4", len(modelTools))
	}
}
