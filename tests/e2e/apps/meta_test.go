package apps_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	client "github.com/panyam/mcpkit/client"
	core "github.com/panyam/mcpkit/core"
	server "github.com/panyam/mcpkit/server"
)

// newAppTestServer creates an MCP server with tools and resources that exercise
// the _meta extension metadata plumbing. Registers both a tool with _meta.ui
// (MCP Apps style) and a plain tool without _meta, plus corresponding resources.
func newAppTestServer() *server.Server {
	srv := server.NewServer(core.ServerInfo{Name: "apps-e2e", Version: "0.1.0"})

	// Tool with _meta.ui — represents an MCP App tool that references a UI resource
	srv.RegisterTool(
		core.ToolDef{
			Name:        "build_deck",
			Description: "Build a slide deck",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"title": map[string]any{"type": "string"}},
			},
			Meta: &core.ToolMeta{
				UI: &core.UIMetadata{
					ResourceUri: "ui://decks/view",
					Visibility:  []core.UIVisibility{core.UIVisibilityModel, core.UIVisibilityApp},
					CSP: &core.UICSPConfig{
						ConnectDomains:  []string{"api.example.com"},
						ResourceDomains: []string{"cdn.example.com"},
					},
					Permissions:   []string{"clipboard-write"},
					PrefersBorder: boolPtr(true),
					Domain:        "slyds",
				},
			},
		},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			var p struct{ Title string `json:"title"` }
			req.Bind(&p)
			return core.TextResult(fmt.Sprintf("built: %s", p.Title)), nil
		},
	)

	// Plain tool without _meta
	srv.RegisterTool(
		core.ToolDef{
			Name:        "echo",
			Description: "Echo input",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("echo"), nil
		},
	)

	// UI resource with per-content _meta
	srv.RegisterResource(
		core.ResourceDef{URI: "ui://decks/view", Name: "Deck Viewer", MimeType: core.AppMIMEType},
		func(ctx context.Context, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{Contents: []core.ResourceReadContent{{
				URI:      req.URI,
				MimeType: core.AppMIMEType,
				Text:     "<html><body>deck viewer</body></html>",
				Meta: &core.ResourceContentMeta{
					UI: &core.UIMetadata{
						ResourceUri: "ui://decks/view",
						Permissions: []string{"clipboard-write"},
					},
				},
			}}}, nil
		},
	)

	// Plain resource without _meta
	srv.RegisterResource(
		core.ResourceDef{URI: "test://plain", Name: "Plain", MimeType: "text/plain"},
		func(ctx context.Context, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{Contents: []core.ResourceReadContent{{
				URI: req.URI, MimeType: "text/plain", Text: "plain content",
			}}}, nil
		},
	)

	return srv
}

// setupClient creates an httptest.Server with Streamable HTTP and a connected client.
func setupClient(t *testing.T) *client.Client {
	t.Helper()
	srv := newAppTestServer()
	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "apps-e2e-client", Version: "1.0"})
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// TestToolsListMetaE2E verifies that _meta on ToolDef survives the full e2e path:
// server registration → HTTP transport → client ListTools deserialization. The _meta
// mechanism is how MCP extensions attach metadata to tools; this test verifies the
// complete round-trip including all UIMetadata fields (resourceUri, visibility, CSP,
// permissions, prefersBorder, domain).
func TestToolsListMetaE2E(t *testing.T) {
	c := setupClient(t)

	tools, err := c.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	// Find build_deck and verify full _meta.ui
	var appTool *core.ToolDef
	for i := range tools {
		if tools[i].Name == "build_deck" {
			appTool = &tools[i]
			break
		}
	}
	if appTool == nil {
		t.Fatal("build_deck not found in tools/list")
	}
	if appTool.Meta == nil {
		t.Fatal("build_deck: Meta is nil — _meta lost in e2e round-trip")
	}
	ui := appTool.Meta.UI
	if ui == nil {
		t.Fatal("build_deck: Meta.UI is nil")
	}
	if ui.ResourceUri != "ui://decks/view" {
		t.Errorf("resourceUri = %q, want %q", ui.ResourceUri, "ui://decks/view")
	}
	if len(ui.Visibility) != 2 {
		t.Errorf("visibility = %v, want [model app]", ui.Visibility)
	}
	if ui.CSP == nil {
		t.Fatal("CSP is nil")
	}
	if len(ui.CSP.ConnectDomains) != 1 || ui.CSP.ConnectDomains[0] != "api.example.com" {
		t.Errorf("CSP.ConnectDomains = %v", ui.CSP.ConnectDomains)
	}
	if len(ui.CSP.ResourceDomains) != 1 || ui.CSP.ResourceDomains[0] != "cdn.example.com" {
		t.Errorf("CSP.ResourceDomains = %v", ui.CSP.ResourceDomains)
	}
	if len(ui.Permissions) != 1 || ui.Permissions[0] != "clipboard-write" {
		t.Errorf("Permissions = %v", ui.Permissions)
	}
	if ui.PrefersBorder == nil || *ui.PrefersBorder != true {
		t.Errorf("PrefersBorder = %v, want true", ui.PrefersBorder)
	}
	if ui.Domain != "slyds" {
		t.Errorf("Domain = %q, want %q", ui.Domain, "slyds")
	}

	// echo tool should NOT have _meta
	for _, tool := range tools {
		if tool.Name == "echo" && tool.Meta != nil {
			t.Error("echo tool should not have _meta")
		}
	}
}

// TestResourcesReadMetaE2E verifies that _meta on ResourceReadContent survives
// the full e2e path: server handler returns content with _meta → HTTP transport
// → client deserializes _meta intact. Also verifies that plain resources without
// _meta produce clean responses with no spurious _meta keys.
func TestResourcesReadMetaE2E(t *testing.T) {
	c := setupClient(t)

	// Resource with _meta
	result, err := c.Call("resources/read", map[string]string{"uri": "ui://decks/view"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	var resp core.ResourceResult
	if err := result.Unmarshal(&resp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(resp.Contents) != 1 {
		t.Fatalf("got %d contents, want 1", len(resp.Contents))
	}
	content := resp.Contents[0]
	if content.MimeType != core.AppMIMEType {
		t.Errorf("mimeType = %q, want %q", content.MimeType, core.AppMIMEType)
	}
	if content.Meta == nil {
		t.Fatal("Meta is nil — _meta lost in e2e round-trip")
	}
	if content.Meta.UI == nil {
		t.Fatal("Meta.UI is nil")
	}
	if content.Meta.UI.ResourceUri != "ui://decks/view" {
		t.Errorf("resourceUri = %q", content.Meta.UI.ResourceUri)
	}
	if len(content.Meta.UI.Permissions) != 1 || content.Meta.UI.Permissions[0] != "clipboard-write" {
		t.Errorf("permissions = %v", content.Meta.UI.Permissions)
	}

	// Plain resource — verify _meta absent at wire level
	result2, err := c.Call("resources/read", map[string]string{"uri": "test://plain"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	var rawResp struct {
		Contents []json.RawMessage `json:"contents"`
	}
	if err := result2.Unmarshal(&rawResp); err != nil {
		t.Fatal(err)
	}
	var rawContent map[string]json.RawMessage
	json.Unmarshal(rawResp.Contents[0], &rawContent)
	if _, ok := rawContent["_meta"]; ok {
		t.Error("plain resource should not have _meta in wire response")
	}
}

func boolPtr(b bool) *bool { return &b }
