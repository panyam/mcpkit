package ui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
)

// TestUIExtensionMetadata verifies that UIExtension returns the correct
// extension metadata matching the MCP Apps spec: extension ID
// "io.modelcontextprotocol/ui", spec version "2026-01-26", stability
// experimental. This ensures the server advertises the right extension
// in the initialize response.
func TestUIExtensionMetadata(t *testing.T) {
	ext := UIExtension{}.Extension()

	if ext.ID != core.UIExtensionID {
		t.Errorf("ID = %q, want %q", ext.ID, core.UIExtensionID)
	}
	if ext.SpecVersion != "2026-01-26" {
		t.Errorf("SpecVersion = %q, want %q", ext.SpecVersion, "2026-01-26")
	}
	if ext.Stability != core.Experimental {
		t.Errorf("Stability = %q, want %q", ext.Stability, core.Experimental)
	}
}

// TestUIExtensionImplementsProvider verifies that UIExtension satisfies
// the core.ExtensionProvider interface at compile time.
func TestUIExtensionImplementsProvider(t *testing.T) {
	var _ core.ExtensionProvider = UIExtension{}
}

// TestUIMetadata_DisplayModeNegotiation probes whether a server can advertise
// which display modes ("inline", "fullscreen", "pip", ...) a tool's UI
// supports, and whether an app can request a display-mode change at runtime.
//
// Without these fields, a host that embeds MCP App iframes has no way of
// knowing whether the app supports fullscreen/pip, so it must either assume
// all modes work (breaking apps that only work inline) or refuse to expose
// mode switching at all (breaking apps that need more real estate). The
// symptom we care about is iframe sizing bugs in hosts like VS Code.
//
// The test constructs a metadata payload with supportedDisplayModes and
// round-trips UIMetadata through JSON. If the field is not defined on the
// struct, json.Unmarshal drops it silently and the re-marshaled payload will
// not contain "supportedDisplayModes" — which is the gap we want to surface.
func TestUIMetadata_DisplayModeNegotiation(t *testing.T) {
	t.Skip("PENDING: gap repro for panyam/mcpkit#185 — unskip when picking up the issue, then fix the gap until the test passes")

	input := []byte(`{"supportedDisplayModes":["inline","fullscreen","pip"]}`)

	var meta core.UIMetadata
	if err := json.Unmarshal(input, &meta); err != nil {
		t.Fatalf("unmarshal UIMetadata: %v", err)
	}

	out, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal UIMetadata: %v", err)
	}

	if !strings.Contains(string(out), "supportedDisplayModes") {
		t.Fatalf("UIMetadata does not carry supportedDisplayModes (got %s) — "+
			"display mode negotiation is not supported; hosts cannot tell which "+
			"display modes an MCP App iframe can handle", out)
	}
}
