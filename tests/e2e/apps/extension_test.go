package apps_test

import (
	"encoding/json"
	"net/http/httptest"
	"sync"
	"testing"

	client "github.com/panyam/mcpkit/client"
	core "github.com/panyam/mcpkit/core"
	server "github.com/panyam/mcpkit/server"
	ui "github.com/panyam/mcpkit/ext/ui"
)

// TestUIExtensionNegotiationE2E verifies the full extension negotiation flow
// end-to-end: server registers UIExtension, client connects, the initialize
// response includes the UI extension metadata, and ClientSupportsUI returns
// true inside a tool handler context. This validates both directions of the
// capability handshake across the wire.
func TestUIExtensionNegotiationE2E(t *testing.T) {
	// Server with UIExtension registered
	srv := server.NewServer(
		core.ServerInfo{Name: "apps-ext-e2e", Version: "0.1.0"},
		server.WithExtension(ui.UIExtension{}),
	)

	// Tool that checks ClientSupportsUI and reports the result
	var mu sync.Mutex
	var uiSupported bool
	srv.RegisterTool(
		core.ToolDef{
			Name:        "check_ui",
			Description: "Reports whether client supports UI",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			mu.Lock()
			uiSupported = core.ClientSupportsUI(ctx)
			mu.Unlock()
			if uiSupported {
				return core.TextResult("ui: yes"), nil
			}
			return core.TextResult("ui: no"), nil
		},
	)

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "ui-test-client", Version: "1.0"},
		client.WithUIExtension(),
	)
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	// Verify server advertised UI extension — client should detect it
	if !c.ServerSupportsUI() {
		t.Error("ServerSupportsUI() should be true")
	}

	// Call the tool to check ClientSupportsUI from handler context
	text, err := c.ToolCall("check_ui", nil)
	if err != nil {
		t.Fatalf("ToolCall: %v", err)
	}
	// Client sends WithUIExtension(), so server-side ClientSupportsUI(ctx) is true
	if text != "ui: yes" {
		t.Errorf("result = %q, want 'ui: yes'", text)
	}
}

// TestServerAdvertisesUIExtensionE2E verifies that a server with UIExtension
// includes "io.modelcontextprotocol/ui" in the initialize response capabilities
// at the wire level.
func TestServerAdvertisesUIExtensionE2E(t *testing.T) {
	srv := server.NewServer(
		core.ServerInfo{Name: "apps-ext-e2e", Version: "0.1.0"},
		server.WithExtension(ui.UIExtension{}),
	)

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "ui-test-client", Version: "1.0"})
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	// The ServerInfo is populated after Connect — verify extensions came through
	// by re-initializing via raw Call and checking the response
	initResult, err := c.Call("initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test", "version": "1.0"},
	})
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}

	var resp struct {
		Capabilities struct {
			Extensions map[string]json.RawMessage `json:"extensions"`
		} `json:"capabilities"`
	}
	if err := initResult.Unmarshal(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Capabilities.Extensions == nil {
		t.Fatal("no extensions in initialize response")
	}
	uiRaw, ok := resp.Capabilities.Extensions[core.UIExtensionID]
	if !ok {
		t.Fatalf("UI extension not in response, got keys: %v", resp.Capabilities.Extensions)
	}

	var uiExt struct {
		SpecVersion string `json:"specVersion"`
		Stability   string `json:"stability"`
	}
	json.Unmarshal(uiRaw, &uiExt)
	if uiExt.SpecVersion != "2026-01-26" {
		t.Errorf("specVersion = %q, want %q", uiExt.SpecVersion, "2026-01-26")
	}
	if uiExt.Stability != "experimental" {
		t.Errorf("stability = %q, want %q", uiExt.Stability, "experimental")
	}
}
