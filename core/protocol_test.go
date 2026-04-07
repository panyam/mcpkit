package core

import (
	"encoding/json"
	"testing"
)

// TestClientCapabilitiesExtensionsParsing verifies that the Extensions field
// on ClientCapabilities correctly deserializes from JSON, including the
// extension ID as map key and the MIMETypes array. This is the server-side
// parsing path: clients send extensions in the initialize request, and the
// server must capture them for runtime checks via ClientSupportsExtension.
func TestClientCapabilitiesExtensionsParsing(t *testing.T) {
	input := `{
		"sampling": {},
		"extensions": {
			"io.modelcontextprotocol/ui": {
				"mimeTypes": ["text/html;profile=mcp-app"]
			}
		}
	}`

	var caps ClientCapabilities
	if err := json.Unmarshal([]byte(input), &caps); err != nil {
		t.Fatal(err)
	}

	if caps.Sampling == nil {
		t.Error("Sampling should be non-nil")
	}
	if caps.Extensions == nil {
		t.Fatal("Extensions should be non-nil")
	}
	uiCap, ok := caps.Extensions[UIExtensionID]
	if !ok {
		t.Fatalf("extension %q not found", UIExtensionID)
	}
	if len(uiCap.MIMETypes) != 1 || uiCap.MIMETypes[0] != AppMIMEType {
		t.Errorf("MIMETypes = %v, want [%s]", uiCap.MIMETypes, AppMIMEType)
	}

	// Round-trip
	data, err := json.Marshal(caps)
	if err != nil {
		t.Fatal(err)
	}
	var got ClientCapabilities
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Extensions[UIExtensionID]; !ok {
		t.Error("extension lost after round-trip")
	}
}

// TestClientCapabilitiesNoExtensions verifies that when no extensions field
// is present in the JSON, the Extensions map is nil (not an empty map).
// This is the common case for clients that don't support any extensions.
func TestClientCapabilitiesNoExtensions(t *testing.T) {
	input := `{"sampling": {}}`

	var caps ClientCapabilities
	if err := json.Unmarshal([]byte(input), &caps); err != nil {
		t.Fatal(err)
	}
	if caps.Extensions != nil {
		t.Errorf("Extensions should be nil when absent, got %v", caps.Extensions)
	}
}

// TestClientExtensionCapMIMETypes verifies that ClientExtensionCap correctly
// handles multiple MIME types and empty arrays through JSON serialization.
func TestClientExtensionCapMIMETypes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{"multiple types", `{"mimeTypes":["text/html","image/svg+xml"]}`, 2},
		{"empty array", `{"mimeTypes":[]}`, 0},
		{"no field", `{}`, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cap ClientExtensionCap
			if err := json.Unmarshal([]byte(tt.input), &cap); err != nil {
				t.Fatal(err)
			}
			if len(cap.MIMETypes) != tt.want {
				t.Errorf("MIMETypes length = %d, want %d", len(cap.MIMETypes), tt.want)
			}
		})
	}
}
