package ui

import (
	"context"
	"testing"

	"github.com/panyam/mcpkit/core"
)

// TestRegisterAppTool verifies that RegisterAppTool registers both a tool with
// _meta.ui metadata and a matching resource with the MCP App MIME type. Uses a
// mock registrar to capture registrations without depending on server/.
func TestRegisterAppTool(t *testing.T) {
	reg := &mockRegistrar{}

	RegisterAppTool(reg, AppToolConfig{
		Name:        "build_deck",
		Description: "Build a slide deck",
		InputSchema: map[string]any{"type": "object"},
		ResourceURI: "ui://decks/view",
		ToolHandler: func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("ok"), nil
		},
		ResourceHandler: func(ctx context.Context, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{}, nil
		},
		Visibility:  []core.UIVisibility{core.UIVisibilityModel, core.UIVisibilityApp},
		Permissions: []string{"clipboard-write"},
	})

	// Verify tool was registered with _meta.ui
	if len(reg.tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(reg.tools))
	}
	td := reg.tools[0]
	if td.Name != "build_deck" {
		t.Errorf("tool name = %q, want %q", td.Name, "build_deck")
	}
	if td.Meta == nil || td.Meta.UI == nil {
		t.Fatal("tool Meta.UI is nil")
	}
	if td.Meta.UI.ResourceUri != "ui://decks/view" {
		t.Errorf("resourceUri = %q, want %q", td.Meta.UI.ResourceUri, "ui://decks/view")
	}
	if len(td.Meta.UI.Visibility) != 2 {
		t.Errorf("visibility length = %d, want 2", len(td.Meta.UI.Visibility))
	}
	if len(td.Meta.UI.Permissions) != 1 || td.Meta.UI.Permissions[0] != "clipboard-write" {
		t.Errorf("permissions = %v", td.Meta.UI.Permissions)
	}

	// Verify resource was registered with correct MIME type
	if len(reg.resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(reg.resources))
	}
	rd := reg.resources[0]
	if rd.URI != "ui://decks/view" {
		t.Errorf("resource URI = %q, want %q", rd.URI, "ui://decks/view")
	}
	if rd.MimeType != core.AppMIMEType {
		t.Errorf("resource MIME = %q, want %q", rd.MimeType, core.AppMIMEType)
	}
}

// TestValidateRefsWarning verifies that ValidateRefs returns a warning when a
// tool references a ui:// resource that has no matching registration.
func TestValidateRefsWarning(t *testing.T) {
	tools := []core.ToolDef{{
		Name: "orphan_tool",
		Meta: &core.ToolMeta{UI: &core.UIMetadata{ResourceUri: "ui://missing/view"}},
	}}

	warnings := UIExtension{}.ValidateRefs(tools, nil, nil)
	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d", len(warnings))
	}
	if warnings[0] == "" {
		t.Error("warning should not be empty")
	}
}

// TestValidateRefsNoWarningExactMatch verifies that no warning is produced when
// a tool's resourceUri exactly matches a registered resource URI.
func TestValidateRefsNoWarningExactMatch(t *testing.T) {
	tools := []core.ToolDef{{
		Name: "matched_tool",
		Meta: &core.ToolMeta{UI: &core.UIMetadata{ResourceUri: "ui://app/view"}},
	}}

	warnings := UIExtension{}.ValidateRefs(tools, []string{"ui://app/view"}, nil)
	if len(warnings) != 0 {
		t.Errorf("expected 0 warnings, got %d: %v", len(warnings), warnings)
	}
}

// TestValidateRefsNoWarningTemplateMatch verifies that no warning is produced
// when a tool's resourceUri matches a registered URI template.
func TestValidateRefsNoWarningTemplateMatch(t *testing.T) {
	tools := []core.ToolDef{{
		Name: "templated_tool",
		Meta: &core.ToolMeta{UI: &core.UIMetadata{ResourceUri: "ui://decks/42/view"}},
	}}

	warnings := UIExtension{}.ValidateRefs(tools, nil, []string{"ui://decks/{id}/view"})
	if len(warnings) != 0 {
		t.Errorf("expected 0 warnings, got %d: %v", len(warnings), warnings)
	}
}

// TestValidateRefsNoMetaSkipped verifies that tools without _meta.ui are
// silently skipped during validation — no warnings, no errors.
func TestValidateRefsNoMetaSkipped(t *testing.T) {
	tools := []core.ToolDef{
		{Name: "plain_tool"},
		{Name: "meta_no_ui", Meta: &core.ToolMeta{}},
	}

	warnings := UIExtension{}.ValidateRefs(tools, nil, nil)
	if len(warnings) != 0 {
		t.Errorf("expected 0 warnings for tools without _meta.ui, got %d", len(warnings))
	}
}

// mockRegistrar captures RegisterTool and RegisterResource calls for testing.
type mockRegistrar struct {
	tools     []core.ToolDef
	resources []core.ResourceDef
}

func (m *mockRegistrar) RegisterTool(def core.ToolDef, _ core.ToolHandler) {
	m.tools = append(m.tools, def)
}

func (m *mockRegistrar) RegisterResource(def core.ResourceDef, _ core.ResourceHandler) {
	m.resources = append(m.resources, def)
}
