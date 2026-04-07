package core

import (
	"context"
	"encoding/json"
	"testing"
)

// TestToolDefMetaSerialization verifies that ToolDef serializes the _meta field
// correctly: present when Meta is set (with nested ui object), absent when Meta
// is nil. This ensures tools/list responses carry UI metadata only when configured.
func TestToolDefMetaSerialization(t *testing.T) {
	t.Run("with meta", func(t *testing.T) {
		td := ToolDef{
			Name:        "build_deck",
			Description: "Build a slide deck",
			InputSchema: map[string]any{"type": "object"},
			Meta: &ToolMeta{
				UI: &UIMetadata{
					ResourceUri: "ui://decks/demo/view",
				},
			},
		}

		data, err := json.Marshal(td)
		if err != nil {
			t.Fatal(err)
		}

		// Verify _meta.ui.resourceUri is present in JSON
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatal(err)
		}
		if _, ok := raw["_meta"]; !ok {
			t.Fatal("expected _meta key in JSON output")
		}

		// Round-trip
		var got ToolDef
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		if got.Meta == nil || got.Meta.UI == nil {
			t.Fatal("Meta or Meta.UI is nil after round-trip")
		}
		if got.Meta.UI.ResourceUri != "ui://decks/demo/view" {
			t.Errorf("ResourceUri = %q, want %q", got.Meta.UI.ResourceUri, "ui://decks/demo/view")
		}
	})

	t.Run("nil meta omitted", func(t *testing.T) {
		td := ToolDef{
			Name:        "simple_tool",
			Description: "No UI",
			InputSchema: map[string]any{"type": "object"},
		}

		data, err := json.Marshal(td)
		if err != nil {
			t.Fatal(err)
		}

		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatal(err)
		}
		if _, ok := raw["_meta"]; ok {
			t.Errorf("_meta key should be absent when Meta is nil, got %s", raw["_meta"])
		}
	})
}

// TestResourceReadContentMetaSerialization verifies that ResourceReadContent
// serializes the _meta field correctly. Per the MCP Apps spec, per-content _meta
// takes precedence over resource-level metadata from resources/list.
func TestResourceReadContentMetaSerialization(t *testing.T) {
	t.Run("with meta", func(t *testing.T) {
		rc := ResourceReadContent{
			URI:      "ui://decks/demo/view",
			MimeType: AppMIMEType,
			Text:     "<html>hello</html>",
			Meta: &ResourceContentMeta{
				UI: &UIMetadata{
					ResourceUri: "ui://decks/demo/view",
					Visibility:  []UIVisibility{UIVisibilityModel, UIVisibilityApp},
				},
			},
		}

		data, err := json.Marshal(rc)
		if err != nil {
			t.Fatal(err)
		}

		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatal(err)
		}
		if _, ok := raw["_meta"]; !ok {
			t.Fatal("expected _meta key in JSON output")
		}

		// Round-trip
		var got ResourceReadContent
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		if got.Meta == nil || got.Meta.UI == nil {
			t.Fatal("Meta or Meta.UI is nil after round-trip")
		}
		if len(got.Meta.UI.Visibility) != 2 {
			t.Errorf("Visibility length = %d, want 2", len(got.Meta.UI.Visibility))
		}
	})

	t.Run("nil meta omitted", func(t *testing.T) {
		rc := ResourceReadContent{
			URI:      "file:///readme.md",
			MimeType: "text/plain",
			Text:     "hello",
		}

		data, err := json.Marshal(rc)
		if err != nil {
			t.Fatal(err)
		}

		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatal(err)
		}
		if _, ok := raw["_meta"]; ok {
			t.Errorf("_meta key should be absent when Meta is nil, got %s", raw["_meta"])
		}
	})
}

// TestUIMetadataJSONRoundTrip verifies that a fully-populated UIMetadata struct
// survives JSON marshal/unmarshal with all fields intact. This covers the complete
// wire format as specified in the MCP Apps extension (io.modelcontextprotocol/ui).
func TestUIMetadataJSONRoundTrip(t *testing.T) {
	border := true
	original := UIMetadata{
		ResourceUri: "ui://myapp/main",
		Visibility:  []UIVisibility{UIVisibilityModel, UIVisibilityApp},
		CSP: &UICSPConfig{
			ConnectDomains:  []string{"api.example.com"},
			ResourceDomains: []string{"cdn.example.com"},
			FrameDomains:    []string{"embed.example.com"},
			BaseUriDomains:  []string{"example.com"},
		},
		Permissions:   []string{"camera", "microphone"},
		PrefersBorder: &border,
		Domain:        "myapp",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}

	var got UIMetadata
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}

	if got.ResourceUri != original.ResourceUri {
		t.Errorf("ResourceUri = %q, want %q", got.ResourceUri, original.ResourceUri)
	}
	if len(got.Visibility) != 2 {
		t.Errorf("Visibility length = %d, want 2", len(got.Visibility))
	}
	if got.CSP == nil {
		t.Fatal("CSP is nil")
	}
	if len(got.CSP.ConnectDomains) != 1 || got.CSP.ConnectDomains[0] != "api.example.com" {
		t.Errorf("CSP.ConnectDomains = %v, want [api.example.com]", got.CSP.ConnectDomains)
	}
	if len(got.CSP.ResourceDomains) != 1 || got.CSP.ResourceDomains[0] != "cdn.example.com" {
		t.Errorf("CSP.ResourceDomains = %v, want [cdn.example.com]", got.CSP.ResourceDomains)
	}
	if len(got.CSP.FrameDomains) != 1 || got.CSP.FrameDomains[0] != "embed.example.com" {
		t.Errorf("CSP.FrameDomains = %v, want [embed.example.com]", got.CSP.FrameDomains)
	}
	if len(got.CSP.BaseUriDomains) != 1 || got.CSP.BaseUriDomains[0] != "example.com" {
		t.Errorf("CSP.BaseUriDomains = %v, want [example.com]", got.CSP.BaseUriDomains)
	}
	if len(got.Permissions) != 2 {
		t.Errorf("Permissions length = %d, want 2", len(got.Permissions))
	}
	if got.PrefersBorder == nil || *got.PrefersBorder != true {
		t.Errorf("PrefersBorder = %v, want true", got.PrefersBorder)
	}
	if got.Domain != "myapp" {
		t.Errorf("Domain = %q, want %q", got.Domain, "myapp")
	}
}

// TestUIMetadataOmitEmpty verifies that zero-value and nil fields in UIMetadata
// are omitted from JSON output. This ensures clean wire format — tools without
// CSP, permissions, or border preferences don't emit empty arrays or null values.
func TestUIMetadataOmitEmpty(t *testing.T) {
	meta := UIMetadata{
		ResourceUri: "ui://minimal",
	}

	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}

	// Only resourceUri should be present
	if _, ok := raw["resourceUri"]; !ok {
		t.Error("resourceUri should be present")
	}
	for _, key := range []string{"visibility", "csp", "permissions", "prefersBorder", "domain"} {
		if _, ok := raw[key]; ok {
			t.Errorf("key %q should be omitted for zero value, got %s", key, raw[key])
		}
	}
}

// TestUICSPConfigSerialization verifies that UICSPConfig correctly serializes
// all four domain lists (connect, resource, frame, baseUri) and round-trips
// through JSON. Hosts use these declarations to construct Content-Security-Policy
// headers for MCP App iframes.
func TestUICSPConfigSerialization(t *testing.T) {
	csp := UICSPConfig{
		ConnectDomains:  []string{"api.example.com", "ws.example.com"},
		ResourceDomains: []string{"cdn.example.com"},
		FrameDomains:    []string{"embed.example.com"},
		BaseUriDomains:  []string{"example.com"},
	}

	data, err := json.Marshal(csp)
	if err != nil {
		t.Fatal(err)
	}

	var got UICSPConfig
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}

	if len(got.ConnectDomains) != 2 {
		t.Errorf("ConnectDomains length = %d, want 2", len(got.ConnectDomains))
	}
	if len(got.ResourceDomains) != 1 {
		t.Errorf("ResourceDomains length = %d, want 1", len(got.ResourceDomains))
	}
	if len(got.FrameDomains) != 1 {
		t.Errorf("FrameDomains length = %d, want 1", len(got.FrameDomains))
	}
	if len(got.BaseUriDomains) != 1 {
		t.Errorf("BaseUriDomains length = %d, want 1", len(got.BaseUriDomains))
	}
}

// TestUIVisibilityConstants verifies that the UIVisibility enum values match
// the MCP Apps spec exactly. "model" means the tool appears in tools/list for
// the LLM; "app" means the tool is callable by apps from the same server.
func TestUIVisibilityConstants(t *testing.T) {
	if UIVisibilityModel != "model" {
		t.Errorf("UIVisibilityModel = %q, want %q", UIVisibilityModel, "model")
	}
	if UIVisibilityApp != "app" {
		t.Errorf("UIVisibilityApp = %q, want %q", UIVisibilityApp, "app")
	}
}

// TestAppMIMEType verifies the MIME type constant matches the MCP Apps spec.
// This profile parameter is what distinguishes MCP App HTML from regular HTML
// resources — hosts use it to decide whether to render in a sandboxed iframe.
func TestAppMIMEType(t *testing.T) {
	if AppMIMEType != "text/html;profile=mcp-app" {
		t.Errorf("AppMIMEType = %q, want %q", AppMIMEType, "text/html;profile=mcp-app")
	}
}

// TestToolDefMetaPrefersBorderTriState verifies the three states of PrefersBorder:
// nil (host decides), true (request border), false (request no border). This is
// a *bool to distinguish "not specified" from "explicitly false".
func TestToolDefMetaPrefersBorderTriState(t *testing.T) {
	tests := []struct {
		name   string
		border *bool
		want   string // expected JSON fragment for prefersBorder
	}{
		{"nil - omitted", nil, ""},
		{"true", boolPtr(true), "true"},
		{"false", boolPtr(false), "false"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta := UIMetadata{
				ResourceUri:   "ui://test",
				PrefersBorder: tt.border,
			}

			data, err := json.Marshal(meta)
			if err != nil {
				t.Fatal(err)
			}

			var raw map[string]json.RawMessage
			if err := json.Unmarshal(data, &raw); err != nil {
				t.Fatal(err)
			}

			if tt.want == "" {
				if _, ok := raw["prefersBorder"]; ok {
					t.Errorf("prefersBorder should be omitted when nil, got %s", raw["prefersBorder"])
				}
			} else {
				got, ok := raw["prefersBorder"]
				if !ok {
					t.Fatalf("prefersBorder should be present")
				}
				if string(got) != tt.want {
					t.Errorf("prefersBorder = %s, want %s", got, tt.want)
				}
			}
		})
	}
}

// TestClientSupportsExtension verifies the context-based extension capability
// check. With a session context containing the UI extension in clientCaps,
// ClientSupportsExtension returns true. Without the extension or without a
// session context, it returns false. This is the general mechanism any
// extension uses to check client support at runtime.
func TestClientSupportsExtension(t *testing.T) {
	caps := &ClientCapabilities{
		Extensions: map[string]ClientExtensionCap{
			UIExtensionID: {MIMETypes: []string{AppMIMEType}},
		},
	}
	ctx := ContextWithSession(context.Background(), nil, nil, nil, caps, nil)

	if !ClientSupportsExtension(ctx, UIExtensionID) {
		t.Error("should return true when extension is in clientCaps")
	}
	if ClientSupportsExtension(ctx, "io.example/nonexistent") {
		t.Error("should return false for unknown extension")
	}
	if ClientSupportsExtension(context.Background(), UIExtensionID) {
		t.Error("should return false with no session context")
	}
}

// TestClientSupportsUI verifies the convenience wrapper that checks for the
// MCP Apps extension specifically. This is what tool handlers call to decide
// whether to include UI-specific content or fall back to text-only responses.
func TestClientSupportsUI(t *testing.T) {
	caps := &ClientCapabilities{
		Extensions: map[string]ClientExtensionCap{
			UIExtensionID: {},
		},
	}
	ctx := ContextWithSession(context.Background(), nil, nil, nil, caps, nil)

	if !ClientSupportsUI(ctx) {
		t.Error("should return true when UI extension is declared")
	}
	if ClientSupportsUI(context.Background()) {
		t.Error("should return false with no session context")
	}
}

// TestUIExtensionIDConstant verifies the extension ID constant matches the
// MCP Apps spec value exactly.
func TestUIExtensionIDConstant(t *testing.T) {
	if UIExtensionID != "io.modelcontextprotocol/ui" {
		t.Errorf("UIExtensionID = %q, want %q", UIExtensionID, "io.modelcontextprotocol/ui")
	}
}

func boolPtr(b bool) *bool { return &b }
