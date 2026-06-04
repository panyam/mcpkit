package skills_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/skills"
)

func TestSkillsExtension_Metadata(t *testing.T) {
	ext := skills.SkillsExtension{}.Extension()
	if ext.ID != skills.ExtensionID {
		t.Errorf("ID = %q, want %q", ext.ID, skills.ExtensionID)
	}
	if ext.SpecVersion == "" {
		t.Errorf("SpecVersion is empty")
	}
	if ext.Stability != core.Experimental {
		t.Errorf("Stability = %q, want experimental", ext.Stability)
	}
}

func TestSkillsExtension_AppearsInInitialize(t *testing.T) {
	_, _, c := boot(t, "testdata/valid")
	if !c.ServerSupportsExtension(skills.ExtensionID) {
		t.Errorf("server should advertise %q in capabilities.extensions", skills.ExtensionID)
	}
}

// TestSkillsExtension_JSONShapeIsObject pins down the regression the PHP
// reference impl hit during SEP-2640 review: capabilities.extensions[ID]
// MUST marshal as the empty JSON object {} not the empty array []. The
// SEP example shows {} and hosts that switch on the value's type would
// reject [] as "not an extension config object".
func TestSkillsExtension_JSONShapeIsObject(t *testing.T) {
	caps := core.ServerCapabilities{
		Extensions: map[string]core.ExtensionCapability{
			skills.ExtensionID: {},
		},
	}
	body, err := json.Marshal(caps)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := string(body)

	// The value for the extension key must be an object literal.
	needle := `"` + skills.ExtensionID + `":{`
	if !strings.Contains(got, needle) {
		t.Errorf("expected substring %q in %s", needle, got)
	}
	// And must not be an array literal.
	wrongNeedle := `"` + skills.ExtensionID + `":[`
	if strings.Contains(got, wrongNeedle) {
		t.Errorf("extension value marshalled as array, want object: %s", got)
	}
}
