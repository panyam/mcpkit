package core

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
)

func TestFileInputDescriptorJSON(t *testing.T) {
	max := 5 * 1024 * 1024
	desc := FileInputDescriptor{
		Accept:  []string{"image/*", ".pdf"},
		MaxSize: &max,
	}
	raw, err := json.Marshal(desc)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"accept":["image/*",".pdf"],"maxSize":5242880}`
	if string(raw) != want {
		t.Errorf("marshal = %s, want %s", raw, want)
	}

	// An empty descriptor must serialize to {}.
	empty, _ := json.Marshal(FileInputDescriptor{})
	if string(empty) != `{}` {
		t.Errorf("empty descriptor = %s, want {}", empty)
	}
}

func TestFileInputProperty(t *testing.T) {
	max := 1024
	prop := FileInputProperty(FileInputDescriptor{Accept: []string{"image/png"}, MaxSize: &max})
	if prop["type"] != "string" || prop["format"] != "uri" {
		t.Errorf("property shape = %v", prop)
	}
	desc, ok := prop[FileInputSchemaKey].(FileInputDescriptor)
	if !ok {
		t.Fatalf("descriptor not embedded under %q", FileInputSchemaKey)
	}
	if desc.Accept[0] != "image/png" || *desc.MaxSize != 1024 {
		t.Errorf("descriptor lost data: %+v", desc)
	}
}

func TestFileInputArrayProperty(t *testing.T) {
	prop := FileInputArrayProperty(FileInputDescriptor{})
	if prop["type"] != "array" {
		t.Errorf("expected array type, got %v", prop["type"])
	}
	items, ok := prop["items"].(map[string]any)
	if !ok {
		t.Fatalf("items is not a map: %T", prop["items"])
	}
	if items["type"] != "string" || items["format"] != "uri" {
		t.Errorf("items shape = %v", items)
	}
	if _, ok := items[FileInputSchemaKey]; !ok {
		t.Errorf("items missing %q", FileInputSchemaKey)
	}
}

func TestExtractFileInputDescriptorFromJSON(t *testing.T) {
	// Simulate JSON unmarshalled into map[string]any.
	raw := `{
		"type": "string",
		"format": "uri",
		"x-mcp-file": {"accept": ["image/png"], "maxSize": 2048}
	}`
	var prop map[string]any
	if err := json.Unmarshal([]byte(raw), &prop); err != nil {
		t.Fatal(err)
	}
	desc := ExtractFileInputDescriptor(prop)
	if desc == nil {
		t.Fatal("descriptor not extracted")
	}
	if len(desc.Accept) != 1 || desc.Accept[0] != "image/png" {
		t.Errorf("accept = %v", desc.Accept)
	}
	if desc.MaxSize == nil || *desc.MaxSize != 2048 {
		t.Errorf("maxSize = %v", desc.MaxSize)
	}
}

func TestExtractFileInputDescriptorFromTypedValue(t *testing.T) {
	prop := FileInputProperty(FileInputDescriptor{Accept: []string{"image/*"}})
	desc := ExtractFileInputDescriptor(prop)
	if desc == nil {
		t.Fatal("descriptor not extracted from typed property")
	}
	if desc.Accept[0] != "image/*" {
		t.Errorf("accept = %v", desc.Accept)
	}
}

func TestExtractFileInputDescriptorAbsent(t *testing.T) {
	prop := map[string]any{"type": "string"}
	if d := ExtractFileInputDescriptor(prop); d != nil {
		t.Errorf("expected nil, got %+v", d)
	}
	if d := ExtractFileInputDescriptor(nil); d != nil {
		t.Errorf("expected nil for nil prop, got %+v", d)
	}
}

func TestHasFileInputs(t *testing.T) {
	if HasFileInputs(context.Background()) {
		t.Error("HasFileInputs true with no session")
	}

	caps := &ClientCapabilities{}
	ctx := ContextWithSession(context.Background(), nil, nil, &atomic.Pointer[LogLevel]{}, caps, nil)
	if HasFileInputs(ctx) {
		t.Error("HasFileInputs true without capability set")
	}

	caps.FileInputs = &struct{}{}
	if !HasFileInputs(ctx) {
		t.Error("HasFileInputs false despite capability set")
	}
}

func TestClientCapabilitiesFileInputsRoundTrip(t *testing.T) {
	input := `{"fileInputs": {}}`
	var caps ClientCapabilities
	if err := json.Unmarshal([]byte(input), &caps); err != nil {
		t.Fatal(err)
	}
	if caps.FileInputs == nil {
		t.Fatal("FileInputs nil after unmarshal")
	}
	out, err := json.Marshal(caps)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"fileInputs":{}}` {
		t.Errorf("round-trip = %s", out)
	}
}
