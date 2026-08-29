package core

import (
	"encoding/json"
	"testing"
)

// TestExtensionCapabilityMarshalsSettingsInline pins the SEP-2133 wire shape:
// the extension identifier maps directly to its settings object. mcpkit used to
// wrap settings in a {id, specVersion, stability, config} envelope, which put
// every setting one level too deep and added three fields the SEP has nowhere
// to put.
func TestExtensionCapabilityMarshalsSettingsInline(t *testing.T) {
	caps := ServerCapabilities{
		Extensions: map[string]ExtensionCapability{
			"io.modelcontextprotocol/skills": {"directoryRead": true},
		},
	}

	raw, err := json.Marshal(caps)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal to map failed: %v", err)
	}

	exts, ok := m["extensions"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities.extensions missing or not an object: %s", raw)
	}
	ext, ok := exts["io.modelcontextprotocol/skills"].(map[string]any)
	if !ok {
		t.Fatalf("extension entry missing or not an object: %s", raw)
	}

	if ext["directoryRead"] != true {
		t.Errorf("extensions[id].directoryRead = %v, want true (settings must sit inline)", ext["directoryRead"])
	}

	for _, envelope := range []string{"config", "specVersion", "stability", "id"} {
		if _, present := ext[envelope]; present {
			t.Errorf("extensions[id].%s is present; SEP-2133 defines no envelope around the settings object", envelope)
		}
	}
}

// TestExtensionCapabilityEmptySettingsMarshalsAsObject covers SEP-2133's "an
// empty object indicates no settings". A nil Go map marshals to null, which
// would be a distinct and wrong wire value, and five of mcpkit's six extensions
// declare no settings so this is the common path rather than an edge case.
func TestExtensionCapabilityEmptySettingsMarshalsAsObject(t *testing.T) {
	caps := ServerCapabilities{
		Extensions: map[string]ExtensionCapability{
			"io.mcpkit/auth": nil,
		},
	}

	raw, err := json.Marshal(caps)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal to map failed: %v", err)
	}

	exts := m["extensions"].(map[string]any)
	ext, ok := exts["io.mcpkit/auth"].(map[string]any)
	if !ok {
		t.Fatalf("settings-free extension marshalled as %#v, want an empty object", exts["io.mcpkit/auth"])
	}
	if len(ext) != 0 {
		t.Errorf("settings-free extension = %v, want {}", ext)
	}
}

// TestExtensionCapabilityRoundTrip verifies a decoded capability reads its
// settings from the top level, which is what client-side helpers like
// skills.Client.SupportsDirectoryRead rely on.
func TestExtensionCapabilityRoundTrip(t *testing.T) {
	const wire = `{"extensions":{"io.modelcontextprotocol/skills":{"directoryRead":true}}}`

	var caps ServerCapabilities
	if err := json.Unmarshal([]byte(wire), &caps); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	ext, ok := caps.Extensions["io.modelcontextprotocol/skills"]
	if !ok {
		t.Fatal("skills extension not decoded")
	}
	if v, _ := ext["directoryRead"].(bool); !v {
		t.Errorf("decoded directoryRead = %v, want true", ext["directoryRead"])
	}
}
