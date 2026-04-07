package apps_test

import (
	"encoding/json"
	"testing"

	core "github.com/panyam/mcpkit/core"
)

// TestUIResourceServesHTML verifies that the ui://dashboard/view resource
// returns content with the MCP App MIME type (text/html;profile=mcp-app).
// Hosts use this MIME type to decide whether to render in a sandboxed iframe.
func TestUIResourceServesHTML(t *testing.T) {
	c := setupConformanceClient(t)

	result, err := c.Call("resources/read", map[string]string{"uri": "ui://dashboard/view"})
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
}

// TestUIResourceContent verifies that the ui:// resource returns non-empty
// HTML content. The content should be a valid HTML document that hosts can
// render in an iframe.
func TestUIResourceContent(t *testing.T) {
	c := setupConformanceClient(t)

	result, err := c.Call("resources/read", map[string]string{"uri": "ui://dashboard/view"})
	if err != nil {
		t.Fatalf("resources/read: %v", err)
	}
	var resp core.ResourceResult
	result.Unmarshal(&resp)

	text := resp.Contents[0].Text
	if text == "" {
		t.Error("resource text is empty")
	}
	if len(text) < 10 {
		t.Errorf("resource text suspiciously short: %q", text)
	}
}

// TestUITemplateResource verifies that parameterized ui:// resources resolve
// via URI templates. The server registers ui://apps/{id}/view as a template,
// and requesting ui://apps/42/view should return HTML with the ID embedded.
func TestUITemplateResource(t *testing.T) {
	c := setupConformanceClient(t)

	result, err := c.Call("resources/read", map[string]string{"uri": "ui://apps/42/view"})
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
	if resp.Contents[0].Text == "" {
		t.Error("template resource text is empty")
	}
}

// TestPlainResourceNoMeta verifies that non-UI resources do not have _meta.ui
// in their response. This ensures _meta is only present when explicitly set.
func TestPlainResourceNoMeta(t *testing.T) {
	c := setupConformanceClient(t)

	result, err := c.Call("resources/read", map[string]string{"uri": "test://plain-resource"})
	if err != nil {
		t.Fatalf("resources/read: %v", err)
	}

	// Check at wire level that _meta is absent
	var raw struct {
		Contents []json.RawMessage `json:"contents"`
	}
	result.Unmarshal(&raw)
	if len(raw.Contents) != 1 {
		t.Fatalf("got %d contents", len(raw.Contents))
	}

	var content map[string]json.RawMessage
	json.Unmarshal(raw.Contents[0], &content)
	if _, ok := content["_meta"]; ok {
		t.Error("plain resource should not have _meta")
	}
}

// TestResourceMetaPrecedence verifies that per-content _meta.ui on
// ResourceReadContent is present and carries its own metadata. Per the
// MCP Apps spec, per-content metadata takes precedence over resource-level
// metadata from resources/list.
func TestResourceMetaPrecedence(t *testing.T) {
	c := setupConformanceClient(t)

	result, err := c.Call("resources/read", map[string]string{"uri": "ui://dashboard/view"})
	if err != nil {
		t.Fatalf("resources/read: %v", err)
	}
	var resp core.ResourceResult
	result.Unmarshal(&resp)

	content := resp.Contents[0]
	if content.Meta == nil || content.Meta.UI == nil {
		t.Fatal("per-content _meta.ui is nil")
	}
	if content.Meta.UI.ResourceUri != "ui://dashboard/view" {
		t.Errorf("per-content resourceUri = %q", content.Meta.UI.ResourceUri)
	}
	if len(content.Meta.UI.Permissions) != 1 || content.Meta.UI.Permissions[0] != "clipboard-write" {
		t.Errorf("per-content permissions = %v", content.Meta.UI.Permissions)
	}
}
