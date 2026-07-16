package skills_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/ext/skills"
)

func TestLoadAllVerifiedSkills(t *testing.T) {
	sc, _ := connectSkillsClient(t, "testdata/valid")
	loaded, err := sc.LoadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) == 0 {
		t.Fatal("want at least one skill from testdata/valid")
	}
	for _, ls := range loaded {
		if ls.Err != nil {
			t.Fatalf("skill %s: %v", ls.Entry.URL, ls.Err)
		}
		if len(ls.Body) == 0 {
			t.Fatalf("skill %s: empty body", ls.Entry.URL)
		}
	}
	for i := 1; i < len(loaded); i++ {
		if loaded[i-1].Entry.URL >= loaded[i].Entry.URL {
			t.Fatalf("results must be URL-ordered: %s before %s", loaded[i-1].Entry.URL, loaded[i].Entry.URL)
		}
	}
}

func TestLoadIndexIsolatesDigestMismatch(t *testing.T) {
	sc, _ := connectSkillsClient(t, "testdata/valid")
	idx, err := sc.ListSkills(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Skills) < 2 {
		t.Fatalf("fixture assumption: want 2+ skills, got %d", len(idx.Skills))
	}
	// Rotate one entry's digest (the TOCTOU shape from the adversarial
	// suite): that skill must fail in isolation while its siblings load.
	idx.Skills[0].Digest = "sha256:" + strings.Repeat("0", 64)

	loaded := sc.LoadIndex(context.Background(), idx)
	var mismatches, ok int
	for _, ls := range loaded {
		switch {
		case ls.Err != nil && errors.Is(ls.Err, skills.ErrDigestMismatch):
			mismatches++
		case ls.Err == nil && len(ls.Body) > 0:
			ok++
		}
	}
	if mismatches != 1 || ok != len(idx.Skills)-1 {
		t.Fatalf("want 1 isolated mismatch and %d loaded, got mismatches=%d ok=%d", len(idx.Skills)-1, mismatches, ok)
	}
}

func TestInstructionsBlockExcludesFailuresAndIsDeterministic(t *testing.T) {
	loaded := []skills.LoadedSkill{
		{Entry: skills.IndexEntry{Name: "alpha", Description: "does alpha", URL: "skill://a/SKILL.md"}, Body: []byte("Use alpha wisely.")},
		{Entry: skills.IndexEntry{Name: "broken", URL: "skill://b/SKILL.md"}, Err: skills.ErrDigestMismatch},
		{Entry: skills.IndexEntry{Name: "gamma", URL: "skill://c/SKILL.md"}, Body: []byte("Gamma steps.")},
	}
	block := skills.InstructionsBlock(loaded)
	for _, want := range []string{"## Skills", "### Skill: alpha", "does alpha", "Use alpha wisely.", "### Skill: gamma"} {
		if !strings.Contains(block, want) {
			t.Fatalf("block missing %q:\n%s", want, block)
		}
	}
	if strings.Contains(block, "broken") {
		t.Fatalf("failed skill must not be injected:\n%s", block)
	}
	if block != skills.InstructionsBlock(loaded) {
		t.Fatal("rendering must be deterministic")
	}

	if got := skills.InstructionsBlock([]skills.LoadedSkill{{Entry: skills.IndexEntry{Name: "x"}, Err: skills.ErrDigestMismatch}}); got != "" {
		t.Fatalf("all-failed batch must render empty, got %q", got)
	}
	if got := skills.InstructionsBlock(nil); got != "" {
		t.Fatalf("empty batch must render empty, got %q", got)
	}
}
