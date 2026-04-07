package ui

import (
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
