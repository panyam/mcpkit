package skills_test

import (
	"testing"

	"github.com/panyam/mcpkit/ext/skills"
)

// resourcesOf builds a manifest of n files, each of the given size, so the
// two limits can be crossed independently.
func resourcesOf(n int, each int64) skills.SkillResources {
	files := make([]skills.SkillResource, n)
	for i := range files {
		size := each
		files[i] = skills.SkillResource{
			URI:    "skill://x/f.md",
			Digest: "sha256:" + "00000000000000000000000000000000000000000000000000000000000000ff",
			Size:   &size,
		}
	}
	return skills.SkillResources{Files: files}
}

func TestCheckLimits_WithinLimitsIsSilent(t *testing.T) {
	entries := []skills.SkillEntry{{
		URI:       "skill://x/SKILL.md",
		Resources: resourcesOf(skills.MaxResourcesPerSkill, 1),
	}}
	if got := skills.CheckLimits(entries); len(got) != 0 {
		t.Errorf("warnings = %v, want none at exactly the limit", got)
	}
}

func TestCheckLimits_TooManyResources(t *testing.T) {
	entries := []skills.SkillEntry{{
		URI:       "skill://x/SKILL.md",
		Resources: resourcesOf(skills.MaxResourcesPerSkill+1, 1),
	}}
	got := skills.CheckLimits(entries)
	if len(got) != 1 {
		t.Fatalf("warnings = %v, want 1", got)
	}
	if got[0].Limit != "resources" {
		t.Errorf("Limit = %q, want resources", got[0].Limit)
	}
	if got[0].Got != skills.MaxResourcesPerSkill+1 {
		t.Errorf("Got = %d, want %d", got[0].Got, skills.MaxResourcesPerSkill+1)
	}
}

func TestCheckLimits_TooLarge(t *testing.T) {
	// Two files that together cross 16 MiB without crossing the entry count.
	entries := []skills.SkillEntry{{
		URI:       "skill://x/SKILL.md",
		Resources: resourcesOf(2, skills.MaxSkillTotalBytes/2+1),
	}}
	got := skills.CheckLimits(entries)
	if len(got) != 1 {
		t.Fatalf("warnings = %v, want 1", got)
	}
	if got[0].Limit != "size" {
		t.Errorf("Limit = %q, want size", got[0].Limit)
	}
}

// TestCheckLimits_DynamicSkipped covers the sentinel: neither count nor total
// is knowable before a read, so there is nothing to check.
func TestCheckLimits_DynamicSkipped(t *testing.T) {
	entries := []skills.SkillEntry{{
		URI:       "skill://x/SKILL.md",
		Resources: skills.DynamicResources(),
	}}
	if got := skills.CheckLimits(entries); len(got) != 0 {
		t.Errorf("warnings = %v, want none for a dynamic manifest", got)
	}
}

// TestCheckLimits_AbsentSizeDoesNotCountTowardTotal pins the interaction with
// a server that omits size: an unknown length contributes zero rather than
// being guessed, so a pre-size server never trips the size warning spuriously.
func TestCheckLimits_AbsentSizeDoesNotCountTowardTotal(t *testing.T) {
	files := resourcesOf(2, skills.MaxSkillTotalBytes)
	files.Files[0].Size = nil
	files.Files[1].Size = nil
	entries := []skills.SkillEntry{{URI: "skill://x/SKILL.md", Resources: files}}
	if got := skills.CheckLimits(entries); len(got) != 0 {
		t.Errorf("warnings = %v, want none when sizes are absent", got)
	}
}

func TestLimitWarning_ErrorMentionsBothValues(t *testing.T) {
	w := skills.LimitWarning{URI: "skill://x/SKILL.md", Limit: "resources", Got: 513, Max: 512}
	msg := w.Error()
	for _, want := range []string{"skill://x/SKILL.md", "513", "512"} {
		if !contains(msg, want) {
			t.Errorf("Error() = %q, missing %q", msg, want)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
