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

// mockRegistrar captures RegisterTool, RegisterResource, and RegisterResourceTemplate calls for testing.
type mockRegistrar struct {
	tools     []core.ToolDef
	resources []core.ResourceDef
	templates []core.ResourceTemplate
}

func (m *mockRegistrar) RegisterTool(def core.ToolDef, _ core.ToolHandler) {
	m.tools = append(m.tools, def)
}

func (m *mockRegistrar) RegisterResource(def core.ResourceDef, _ core.ResourceHandler) {
	m.resources = append(m.resources, def)
}

func (m *mockRegistrar) RegisterResourceTemplate(def core.ResourceTemplate, _ core.TemplateHandler) {
	m.templates = append(m.templates, def)
}

// TestRegisterAppToolTemplate verifies that RegisterAppTool detects a template
// URI (contains "{") and routes registration to RegisterResourceTemplate instead
// of RegisterResource.
func TestRegisterAppToolTemplate(t *testing.T) {
	reg := &mockRegistrar{}

	RegisterAppTool(reg, AppToolConfig{
		Name:        "show_pizza",
		Description: "Show a pizza",
		InputSchema: map[string]any{"type": "object"},
		ResourceURI: "ui://pizzas/{pizzaId}/details",
		ToolHandler: func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("ok"), nil
		},
		TemplateHandler: func(ctx context.Context, uri string, params map[string]string) (core.ResourceResult, error) {
			return core.ResourceResult{}, nil
		},
	})

	if len(reg.templates) != 1 {
		t.Fatalf("expected 1 template, got %d", len(reg.templates))
	}
	if len(reg.resources) != 0 {
		t.Errorf("expected 0 resources, got %d", len(reg.resources))
	}
	tmpl := reg.templates[0]
	if tmpl.URITemplate != "ui://pizzas/{pizzaId}/details" {
		t.Errorf("URITemplate = %q, want %q", tmpl.URITemplate, "ui://pizzas/{pizzaId}/details")
	}
	if tmpl.MimeType != core.AppMIMEType {
		t.Errorf("MimeType = %q, want %q", tmpl.MimeType, core.AppMIMEType)
	}

	// Tool should still have the correct _meta.ui.resourceUri
	if len(reg.tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(reg.tools))
	}
	if reg.tools[0].Meta == nil || reg.tools[0].Meta.UI == nil {
		t.Fatal("tool Meta.UI is nil")
	}
	if reg.tools[0].Meta.UI.ResourceUri != "ui://pizzas/{pizzaId}/details" {
		t.Errorf("resourceUri = %q, want template URI", reg.tools[0].Meta.UI.ResourceUri)
	}
}

// TestRegisterAppToolTemplateNilHandlerPanics verifies that RegisterAppTool
// panics when a template URI is used without a TemplateHandler.
func TestRegisterAppToolTemplateNilHandlerPanics(t *testing.T) {
	reg := &mockRegistrar{}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for template URI without TemplateHandler")
		}
	}()

	RegisterAppTool(reg, AppToolConfig{
		Name:        "bad_template",
		ResourceURI: "ui://items/{id}/view",
		ToolHandler: func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("ok"), nil
		},
		// TemplateHandler intentionally nil
	})
}

// TestRegisterAppToolConcreteNilHandlerPanics verifies that RegisterAppTool
// panics when a concrete URI is used without a ResourceHandler.
func TestRegisterAppToolConcreteNilHandlerPanics(t *testing.T) {
	reg := &mockRegistrar{}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for concrete URI without ResourceHandler")
		}
	}()

	RegisterAppTool(reg, AppToolConfig{
		Name:        "bad_concrete",
		ResourceURI: "ui://items/view",
		ToolHandler: func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("ok"), nil
		},
		// ResourceHandler intentionally nil
	})
}

// TestRegisterAppToolSupportedDisplayModes verifies that SupportedDisplayModes
// flows through from AppToolConfig to the tool's _meta.ui.
func TestRegisterAppToolSupportedDisplayModes(t *testing.T) {
	reg := &mockRegistrar{}

	RegisterAppTool(reg, AppToolConfig{
		Name:        "dashboard",
		ResourceURI: "ui://dashboard/view",
		ToolHandler: func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("ok"), nil
		},
		ResourceHandler: func(ctx context.Context, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{}, nil
		},
		SupportedDisplayModes: []core.DisplayMode{core.DisplayModeInline, core.DisplayModeFullscreen},
	})

	if len(reg.tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(reg.tools))
	}
	ui := reg.tools[0].Meta.UI
	if len(ui.SupportedDisplayModes) != 2 {
		t.Fatalf("SupportedDisplayModes length = %d, want 2", len(ui.SupportedDisplayModes))
	}
	if ui.SupportedDisplayModes[0] != core.DisplayModeInline {
		t.Errorf("SupportedDisplayModes[0] = %q, want %q", ui.SupportedDisplayModes[0], core.DisplayModeInline)
	}
	if ui.SupportedDisplayModes[1] != core.DisplayModeFullscreen {
		t.Errorf("SupportedDisplayModes[1] = %q, want %q", ui.SupportedDisplayModes[1], core.DisplayModeFullscreen)
	}
}
