package ui

import (
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
		ToolHandler: func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("ok"), nil
		},
		ResourceHandler: func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
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
	tools            []core.ToolDef
	toolHandlers     []core.ToolHandler
	resources        []core.ResourceDef
	resourceHandlers []core.ResourceHandler
	templates        []core.ResourceTemplate
	templateHandlers []core.TemplateHandler
}

func (m *mockRegistrar) RegisterTool(def core.ToolDef, h core.ToolHandler) {
	m.tools = append(m.tools, def)
	m.toolHandlers = append(m.toolHandlers, h)
}

func (m *mockRegistrar) RegisterResource(def core.ResourceDef, h core.ResourceHandler) {
	m.resources = append(m.resources, def)
	m.resourceHandlers = append(m.resourceHandlers, h)
}

func (m *mockRegistrar) RegisterResourceTemplate(def core.ResourceTemplate, h core.TemplateHandler) {
	m.templates = append(m.templates, def)
	m.templateHandlers = append(m.templateHandlers, h)
}

// TestRegisterAppToolTemplate verifies that RegisterAppTool detects a template
// URI, registers both a template resource and a concrete fallback, and
// advertises the concrete fallback URI in the tool's _meta.ui.resourceUri
// so hosts that don't support template variable substitution can fetch it.
func TestRegisterAppToolTemplate(t *testing.T) {
	reg := &mockRegistrar{}

	RegisterAppTool(reg, AppToolConfig{
		Name:        "show_pizza",
		Description: "Show a pizza",
		InputSchema: map[string]any{"type": "object"},
		ResourceURI: "ui://pizzas/{pizzaId}/details",
		ToolHandler: func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("ok"), nil
		},
		TemplateHandler: func(ctx core.ResourceContext, uri string, params map[string]string) (core.ResourceResult, error) {
			return core.ResourceResult{}, nil
		},
	})

	// Template resource is always registered for smart clients.
	if len(reg.templates) != 1 {
		t.Fatalf("expected 1 template, got %d", len(reg.templates))
	}
	tmpl := reg.templates[0]
	if tmpl.URITemplate != "ui://pizzas/{pizzaId}/details" {
		t.Errorf("URITemplate = %q, want %q", tmpl.URITemplate, "ui://pizzas/{pizzaId}/details")
	}
	if tmpl.MimeType != core.AppMIMEType {
		t.Errorf("MimeType = %q, want %q", tmpl.MimeType, core.AppMIMEType)
	}

	// Concrete fallback resource is auto-generated for current hosts.
	wantConcreteURI := "ui://pizzas/show_pizza/latest"
	if len(reg.resources) != 1 {
		t.Fatalf("expected 1 concrete fallback resource, got %d", len(reg.resources))
	}
	if reg.resources[0].URI != wantConcreteURI {
		t.Errorf("concrete resource URI = %q, want %q", reg.resources[0].URI, wantConcreteURI)
	}

	// Tool's _meta.ui.resourceUri should point to the concrete fallback.
	if len(reg.tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(reg.tools))
	}
	if reg.tools[0].Meta == nil || reg.tools[0].Meta.UI == nil {
		t.Fatal("tool Meta.UI is nil")
	}
	if reg.tools[0].Meta.UI.ResourceUri != wantConcreteURI {
		t.Errorf("resourceUri = %q, want %q", reg.tools[0].Meta.UI.ResourceUri, wantConcreteURI)
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
		ToolHandler: func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
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
		ToolHandler: func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
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
		ToolHandler: func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("ok"), nil
		},
		ResourceHandler: func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
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

// TestTemplateManualHybrid verifies that providing a template URI with a
// ResourceHandler (no TemplateHandler) falls through to the manual hybrid
// path — no auto-generated fallback, consumer owns the concrete resource.
func TestTemplateManualHybrid(t *testing.T) {
	reg := &mockRegistrar{}

	RegisterAppTool(reg, AppToolConfig{
		Name:        "manual_tool",
		Description: "Manual hybrid",
		InputSchema: map[string]any{"type": "object"},
		ResourceURI: "ui://app/{id}/view",
		ToolHandler: func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("ok"), nil
		},
		ResourceHandler: func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{}, nil
		},
		// TemplateHandler intentionally nil — manual hybrid
	})

	// No template registered (manual pattern owns everything).
	if len(reg.templates) != 0 {
		t.Errorf("expected 0 templates, got %d", len(reg.templates))
	}
	// Resource registered with the original template URI as-is.
	if len(reg.resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(reg.resources))
	}
	if reg.resources[0].URI != "ui://app/{id}/view" {
		t.Errorf("resource URI = %q, want %q", reg.resources[0].URI, "ui://app/{id}/view")
	}
	// Tool's resourceUri is the original (consumer manages it).
	if reg.tools[0].Meta.UI.ResourceUri != "ui://app/{id}/view" {
		t.Errorf("resourceUri = %q, want original template URI", reg.tools[0].Meta.UI.ResourceUri)
	}
}

// TestConcreteFallbackURI verifies the synthetic URI generation.
func TestConcreteFallbackURI(t *testing.T) {
	tests := []struct {
		templateURI string
		toolName    string
		want        string
	}{
		{"ui://slyds/decks/{deck}/preview", "preview_deck", "ui://slyds/preview_deck/latest"},
		{"ui://pizzas/{pizzaId}/details", "show_pizza", "ui://pizzas/show_pizza/latest"},
		{"ui://app/{a}/{b}/view", "my_tool", "ui://app/my_tool/latest"},
		{"not-a-uri", "tool", "ui://tool/latest"}, // malformed URI fallback
	}
	for _, tt := range tests {
		got := concreteFallbackURI(tt.templateURI, tt.toolName)
		if got != tt.want {
			t.Errorf("concreteFallbackURI(%q, %q) = %q, want %q", tt.templateURI, tt.toolName, got, tt.want)
		}
	}
}

// TestExtractTemplateParams verifies extraction of template variable values
// from tool arguments JSON.
func TestExtractTemplateParams(t *testing.T) {
	tests := []struct {
		name string
		vars []string
		args string
		want map[string]string
	}{
		{
			name: "string values",
			vars: []string{"deck", "slide"},
			args: `{"deck": "q3-review", "slide": "intro", "extra": "ignored"}`,
			want: map[string]string{"deck": "q3-review", "slide": "intro"},
		},
		{
			name: "numeric value stringified",
			vars: []string{"id"},
			args: `{"id": 42}`,
			want: map[string]string{"id": "42"},
		},
		{
			name: "missing var",
			vars: []string{"deck", "missing"},
			args: `{"deck": "hello"}`,
			want: map[string]string{"deck": "hello"},
		},
		{
			name: "empty args",
			vars: []string{"x"},
			args: ``,
			want: map[string]string{},
		},
		{
			name: "null args",
			vars: []string{"x"},
			args: `null`,
			want: map[string]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractTemplateParams(tt.vars, []byte(tt.args))
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("params[%q] = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}
