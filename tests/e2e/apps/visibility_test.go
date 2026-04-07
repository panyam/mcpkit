package apps_test

import (
	"testing"
)

// TestListToolsIncludesAll verifies that tools/list returns ALL tools including
// app-only tools. The MCP spec places visibility filtering responsibility on the
// host, not the server — servers always return the complete tool set.
func TestListToolsIncludesAll(t *testing.T) {
	c := setupConformanceClient(t)

	tools, err := c.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Name] = true
	}

	// All tools must be present, including app-only
	for _, want := range []string{"show-dashboard", "navigate-dashboard", "dashboard-data", "mutate-dashboard", "plain-tool"} {
		if !names[want] {
			t.Errorf("tools/list should include %q", want)
		}
	}
}

// TestListToolsForModelFilters verifies that ListToolsForModel excludes tools
// with visibility ["app"] only, while including tools with default visibility,
// visibility ["model"], or visibility ["model", "app"].
func TestListToolsForModelFilters(t *testing.T) {
	c := setupConformanceClient(t)

	modelTools, err := c.ListToolsForModel()
	if err != nil {
		t.Fatalf("ListToolsForModel: %v", err)
	}

	names := make(map[string]bool)
	for _, tool := range modelTools {
		names[tool.Name] = true
	}

	// These should be included (model-visible)
	for _, want := range []string{"show-dashboard", "dashboard-data", "mutate-dashboard", "plain-tool"} {
		if !names[want] {
			t.Errorf("ListToolsForModel should include %q", want)
		}
	}

	// This should be excluded (app-only)
	if names["navigate-dashboard"] {
		t.Error("ListToolsForModel should NOT include navigate-dashboard (app-only)")
	}
}

// TestAppOnlyToolCallable verifies that app-only tools (visibility: ["app"])
// can still be called via tools/call. Visibility is a presentation hint for
// hosts, not an access control mechanism — the server executes all tools.
func TestAppOnlyToolCallable(t *testing.T) {
	c := setupConformanceClient(t)

	text, err := c.ToolCall("navigate-dashboard", map[string]string{"page": "settings"})
	if err != nil {
		t.Fatalf("ToolCall navigate-dashboard: %v", err)
	}
	if text != "Navigated to settings" {
		t.Errorf("result = %q, want 'Navigated to settings'", text)
	}
}
