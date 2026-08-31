package skills_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/ext/skills"
)

// entry is a minimal SkillEntry for the pure-rendering tests, which care only
// about frontmatter and URI.
func entry(name, description, uri string) skills.SkillEntry {
	fm := map[string]any{"name": name}
	if description != "" {
		fm["description"] = description
	}
	return skills.SkillEntry{URI: uri, Frontmatter: fm}
}

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
			t.Fatalf("skill %s: %v", ls.Entry.URI, ls.Err)
		}
		if len(ls.Body) == 0 {
			t.Fatalf("skill %s: empty body", ls.Entry.URI)
		}
	}
	for i := 1; i < len(loaded); i++ {
		if loaded[i-1].Entry.URI >= loaded[i].Entry.URI {
			t.Fatalf("results must be URI-ordered: %s before %s", loaded[i-1].Entry.URI, loaded[i].Entry.URI)
		}
	}
}

// TestLoadEntriesIsolatesDigestMismatch is the TOCTOU shape from the
// adversarial suite: rotating one entry's pinned digest must fail that skill
// alone while its siblings still load.
func TestLoadEntriesIsolatesDigestMismatch(t *testing.T) {
	sc, _ := connectSkillsClient(t, "testdata/valid")
	entries, err := sc.ListSkillEntries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 2 {
		t.Fatalf("fixture assumption: want 2+ skills, got %d", len(entries))
	}

	// Rotate the pin covering the first entry's own SKILL.md.
	entries[0].Resources.Files = append([]skills.SkillResource(nil), entries[0].Resources.Files...)
	for i, f := range entries[0].Resources.Files {
		if f.URI == entries[0].URI {
			entries[0].Resources.Files[i].Digest = "sha256:" + strings.Repeat("0", 64)
		}
	}

	loaded := sc.LoadEntries(context.Background(), entries)
	var mismatches, ok int
	for _, ls := range loaded {
		switch {
		case ls.Err != nil && errors.Is(ls.Err, skills.ErrDigestMismatch):
			mismatches++
		case ls.Err == nil && len(ls.Body) > 0:
			ok++
		}
	}
	if mismatches != 1 || ok != len(entries)-1 {
		t.Fatalf("want 1 isolated mismatch and %d loaded, got mismatches=%d ok=%d", len(entries)-1, mismatches, ok)
	}
}

func TestInstructionsBlockExcludesFailuresAndIsDeterministic(t *testing.T) {
	loaded := []skills.LoadedSkill{
		{Entry: entry("alpha", "does alpha", "skill://a/SKILL.md"), Body: []byte("Use alpha wisely.")},
		{Entry: entry("broken", "", "skill://b/SKILL.md"), Err: skills.ErrDigestMismatch},
		{Entry: entry("gamma", "", "skill://c/SKILL.md"), Body: []byte("Gamma steps.")},
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

	if got := skills.InstructionsBlock([]skills.LoadedSkill{{Entry: entry("x", "", ""), Err: skills.ErrDigestMismatch}}); got != "" {
		t.Fatalf("all-failed batch must render empty, got %q", got)
	}
	if got := skills.InstructionsBlock(nil); got != "" {
		t.Fatalf("empty batch must render empty, got %q", got)
	}
}

func TestCatalogBlock(t *testing.T) {
	entries := []skills.SkillEntry{
		entry("alpha", "does alpha", "skill://a/SKILL.md"),
		entry("beta", "", "skill://b/SKILL.md"),
	}
	block := skills.CatalogBlock(entries)
	if !strings.Contains(block, "## Skills (catalog)") || !strings.Contains(block, "load_skill") {
		t.Fatalf("catalog header/hint missing:\n%s", block)
	}
	if !strings.Contains(block, "- alpha: does alpha") {
		t.Fatalf("named+described skill missing:\n%s", block)
	}
	if !strings.Contains(block, "- beta\n") {
		t.Fatalf("description-less skill should still list:\n%s", block)
	}
	if got := skills.CatalogBlock(nil); got != "" {
		t.Fatalf("empty catalog = %q, want \"\"", got)
	}
}
