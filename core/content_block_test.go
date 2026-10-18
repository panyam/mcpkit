package core

import (
	"encoding/json"
	"testing"
)

// TestResourceLinkContentWireShape pins the "resource_link" block (2025-06-18)
// to the spec's Resource keys, flat on the block rather than under "resource".
func TestResourceLinkContentWireShape(t *testing.T) {
	size := int64(1024)
	c := ResourceLinkContent(ResourceDef{
		URI: "file:///readme", Name: "readme", Title: "Readme", Description: "Project readme",
		MimeType: "text/markdown", Size: &size, Icons: []Icon{{Src: "https://example.com/i.png"}},
		Annotations: map[string]any{"priority": 0.5},
		Meta:        &ResourceMeta{UI: &UIMetadata{ResourceUri: "ui://x"}},
	})
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := map[string]any{
		"type": "resource_link", "uri": "file:///readme", "name": "readme", "title": "Readme",
		"description": "Project readme", "mimeType": "text/markdown", "size": float64(1024),
	}
	for k, v := range want {
		if obj[k] != v {
			t.Errorf("%s = %v, want %v (raw %s)", k, obj[k], v, raw)
		}
	}
	if icons, _ := obj["icons"].([]any); len(icons) != 1 {
		t.Errorf("icons = %v, want one", obj["icons"])
	}
	if ann, _ := obj["annotations"].(map[string]any); ann["priority"] != 0.5 {
		t.Errorf("annotations = %v", obj["annotations"])
	}
	for _, k := range []string{"resource", "_meta", "text", "data"} {
		if _, present := obj[k]; present {
			t.Errorf("resource_link carries %q: %s", k, raw)
		}
	}
}

// TestContentDecodesEveryField guards the custom UnmarshalJSON, which used
// to copy fields by name and so would drop any field it was not told about.
func TestContentDecodesEveryField(t *testing.T) {
	raw := `{"type":"resource_link","uri":"file:///r","name":"r","title":"R","description":"d",
		"mimeType":"text/plain","size":0,"icons":[{"src":"https://example.com/i.png","theme":"dark"}],
		"annotations":{"audience":["user"]},"_meta":{"com.example/k":"v"}}`
	var c Content
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if c.Type != "resource_link" || c.URI != "file:///r" || c.Name != "r" || c.Title != "R" ||
		c.Description != "d" || c.MimeType != "text/plain" {
		t.Errorf("scalar fields = %+v", c)
	}
	if c.Size == nil || *c.Size != 0 {
		t.Errorf("size = %v, want present 0", c.Size)
	}
	if len(c.Icons) != 1 || c.Icons[0].Theme != IconThemeDark {
		t.Errorf("icons = %+v", c.Icons)
	}
	if aud, _ := c.Annotations["audience"].([]any); len(aud) != 1 || aud[0] != "user" {
		t.Errorf("annotations = %v", c.Annotations)
	}
	if c.Meta["com.example/k"] != "v" {
		t.Errorf("_meta = %v", c.Meta)
	}
}

// TestContentDecodeResetsReceiver checks that decoding into a reused Content
// does not leak fields from the previous value.
func TestContentDecodeResetsReceiver(t *testing.T) {
	c := Content{Type: "text", Text: "old", URI: "file:///old", Resource: &ResourceContent{URI: "file:///old"}}
	if err := json.Unmarshal([]byte(`{"type":"image","data":"AA==","mimeType":"image/png"}`), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if c.Text != "" || c.URI != "" || c.Resource != nil {
		t.Errorf("stale fields survived decode: %+v", c)
	}
}
