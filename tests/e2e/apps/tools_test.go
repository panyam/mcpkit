package apps_test

import (
	"testing"

	core "github.com/panyam/mcpkit/core"
)

// TestToolMetaResourceUri verifies that the show-dashboard tool carries
// _meta.ui.resourceUri in the tools/list response. This is how hosts discover
// which resource to fetch for rendering the tool's UI.
func TestToolMetaResourceUri(t *testing.T) {
	c := setupConformanceClient(t)

	tools, err := c.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	tool := findTool(t, tools, "show-dashboard")
	if tool.Meta == nil || tool.Meta.UI == nil {
		t.Fatal("show-dashboard: _meta.ui is nil")
	}
	if tool.Meta.UI.ResourceUri != "ui://dashboard/view" {
		t.Errorf("resourceUri = %q, want %q", tool.Meta.UI.ResourceUri, "ui://dashboard/view")
	}
}

// TestToolMetaVisibilityDefaults verifies that a tool without explicit visibility
// metadata is included by ListToolsForModel (default = visible to both model and
// app). This ensures backward compatibility — existing tools work without UI config.
func TestToolMetaVisibilityDefaults(t *testing.T) {
	c := setupConformanceClient(t)

	modelTools, err := c.ListToolsForModel()
	if err != nil {
		t.Fatalf("ListToolsForModel: %v", err)
	}

	if findToolOptional(modelTools, "plain-tool") == nil {
		t.Error("plain-tool (no visibility set) should be included in ListToolsForModel")
	}
}

// TestToolMetaCSP verifies that CSP (Content-Security-Policy) declarations on
// a tool's _meta.ui survive the wire round-trip. Hosts use these to construct
// the CSP header for the app's iframe sandbox.
func TestToolMetaCSP(t *testing.T) {
	c := setupConformanceClient(t)

	tools, err := c.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	tool := findTool(t, tools, "show-dashboard")
	if tool.Meta.UI.CSP == nil {
		t.Fatal("CSP is nil")
	}
	if len(tool.Meta.UI.CSP.ResourceDomains) != 1 || tool.Meta.UI.CSP.ResourceDomains[0] != "cdn.example.com" {
		t.Errorf("CSP.ResourceDomains = %v, want [cdn.example.com]", tool.Meta.UI.CSP.ResourceDomains)
	}
}

// TestToolMetaPermissions verifies that the permissions array on _meta.ui
// survives the wire round-trip. Hosts use these to set iframe permission policy.
func TestToolMetaPermissions(t *testing.T) {
	c := setupConformanceClient(t)

	tools, err := c.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	tool := findTool(t, tools, "show-dashboard")
	if len(tool.Meta.UI.Permissions) != 1 || tool.Meta.UI.Permissions[0] != "clipboard-write" {
		t.Errorf("Permissions = %v, want [clipboard-write]", tool.Meta.UI.Permissions)
	}
}

// TestToolMetaPrefersBorder verifies that the prefersBorder tri-state field
// survives the wire round-trip. The show-dashboard tool sets it to false,
// hinting that the host should not draw a border around the iframe.
func TestToolMetaPrefersBorder(t *testing.T) {
	c := setupConformanceClient(t)

	tools, err := c.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	tool := findTool(t, tools, "show-dashboard")
	if tool.Meta.UI.PrefersBorder == nil {
		t.Fatal("PrefersBorder is nil, want false")
	}
	if *tool.Meta.UI.PrefersBorder != false {
		t.Errorf("PrefersBorder = %v, want false", *tool.Meta.UI.PrefersBorder)
	}
}

// findTool returns the tool with the given name, or fails the test.
func findTool(t *testing.T, tools []core.ToolDef, name string) core.ToolDef {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("tool %q not found in tools/list (%d tools)", name, len(tools))
	return core.ToolDef{}
}

// findToolOptional returns a pointer to the tool with the given name, or nil.
func findToolOptional(tools []core.ToolDef, name string) *core.ToolDef {
	for i := range tools {
		if tools[i].Name == name {
			return &tools[i]
		}
	}
	return nil
}
