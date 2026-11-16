package skills_test

import (
	"encoding/json"
	"testing"

	"github.com/panyam/mcpkit/ext/skills"
)

// TestSkillsListResult_InvalidEntryDoesNotSinkTheListing covers SEP-2640's
// per-entry rule: an entry whose resources is absent, or neither an array nor
// "dynamic", is invalid, and a host MUST NOT load it. The other entries in
// the same listing stay loadable, so decoding keeps every entry and leaves
// the invalid ones with zero Resources, which ReadFromEntry refuses with
// ErrInvalidResources.
func TestSkillsListResult_InvalidEntryDoesNotSinkTheListing(t *testing.T) {
	raw := `{"skills": [
		{"uri": "skill://ok/SKILL.md", "frontmatter": {"name": "ok", "description": "d"},
		 "resources": [{"uri": "skill://ok/SKILL.md", "digest": "sha256:00", "size": 1}]},
		{"uri": "skill://none/SKILL.md", "frontmatter": {"name": "none", "description": "d"}},
		{"uri": "skill://bad/SKILL.md", "frontmatter": {"name": "bad", "description": "d"}, "resources": "all"},
		{"uri": "skill://num/SKILL.md", "frontmatter": {"name": "num", "description": "d"}, "resources": 5}
	]}`
	var lr skills.SkillsListResult
	if err := json.Unmarshal([]byte(raw), &lr); err != nil {
		t.Fatalf("one invalid entry failed the whole listing: %v", err)
	}
	if len(lr.Skills) != 4 {
		t.Fatalf("decoded %d entries, want 4", len(lr.Skills))
	}
	if len(lr.Skills[0].Resources.Files) != 1 {
		t.Errorf("the valid entry lost its resources: %+v", lr.Skills[0].Resources)
	}
	for _, e := range lr.Skills[1:] {
		if e.Resources.Dynamic || e.Resources.Files != nil {
			t.Errorf("%s: invalid resources decoded as %+v, want the zero value", e.URI, e.Resources)
		}
	}
}
