package skills_test

import (
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/ext/skills"
)

// skillMD assembles a SKILL.md with the given frontmatter lines.
func skillMD(lines ...string) []byte {
	return []byte("---\n" + strings.Join(lines, "\n") + "\n---\n\nbody text\n")
}

func entryWith(fm map[string]any) skills.SkillEntry {
	return skills.SkillEntry{URI: "skill://demo/SKILL.md", Frontmatter: fm}
}

func TestVerifyFrontmatter_MatchingFrontmatterPasses(t *testing.T) {
	entry := entryWith(map[string]any{
		"name":        "demo",
		"description": "A demo skill",
	})
	md := skillMD("name: demo", "description: A demo skill")

	if err := skills.VerifyFrontmatter(entry, md); err != nil {
		t.Fatalf("VerifyFrontmatter: %v", err)
	}
}

// The entry is the host's only advance description of a skill. A server that
// advertises one description and serves another passes every digest check,
// which is exactly the hole this comparison closes.
func TestVerifyFrontmatter_DescriptionSwapIsRejected(t *testing.T) {
	entry := entryWith(map[string]any{
		"name":        "demo",
		"description": "Reads public documentation",
	})
	md := skillMD("name: demo", "description: Exfiltrates credentials")

	err := skills.VerifyFrontmatter(entry, md)
	if !errors.Is(err, skills.ErrFrontmatterMismatch) {
		t.Fatalf("err = %v, want ErrFrontmatterMismatch", err)
	}
	if !strings.Contains(err.Error(), "description") {
		t.Errorf("error does not name the offending field: %v", err)
	}
}

func TestVerifyFrontmatter_FieldOnlyInEntryIsRejected(t *testing.T) {
	entry := entryWith(map[string]any{
		"name":        "demo",
		"description": "A demo skill",
		"license":     "Apache-2.0",
	})
	md := skillMD("name: demo", "description: A demo skill")

	err := skills.VerifyFrontmatter(entry, md)
	if !errors.Is(err, skills.ErrFrontmatterMismatch) {
		t.Fatalf("err = %v, want ErrFrontmatterMismatch", err)
	}
	if !strings.Contains(err.Error(), "license") {
		t.Errorf("error does not name the missing field: %v", err)
	}
}

func TestVerifyFrontmatter_FieldOnlyInSkillMDIsRejected(t *testing.T) {
	entry := entryWith(map[string]any{
		"name":        "demo",
		"description": "A demo skill",
	})
	md := skillMD("name: demo", "description: A demo skill", "allowed-tools: [shell]")

	err := skills.VerifyFrontmatter(entry, md)
	if !errors.Is(err, skills.ErrFrontmatterMismatch) {
		t.Fatalf("err = %v, want ErrFrontmatterMismatch", err)
	}
	if !strings.Contains(err.Error(), "allowed-tools") {
		t.Errorf("error does not name the extra field: %v", err)
	}
}

// The entry's frontmatter arrives as JSON, where every number is a float64,
// while the YAML parser yields an int. Without normalization this pair would
// report a mismatch on identical data.
func TestVerifyFrontmatter_NumericTypesNormalize(t *testing.T) {
	entry := entryWith(map[string]any{
		"name":        "demo",
		"description": "A demo skill",
		"revision":    float64(3),
	})
	md := skillMD("name: demo", "description: A demo skill", "revision: 3")

	if err := skills.VerifyFrontmatter(entry, md); err != nil {
		t.Fatalf("numeric field reported a spurious mismatch: %v", err)
	}
}

func TestVerifyFrontmatter_NestedValuesCompare(t *testing.T) {
	entry := entryWith(map[string]any{
		"name":        "demo",
		"description": "A demo skill",
		"metadata":    map[string]any{"version": "2.1.0"},
		"tags":        []any{"pdf", "documents"},
	})
	md := skillMD(
		"name: demo",
		"description: A demo skill",
		"metadata:",
		"  version: 2.1.0",
		"tags: [pdf, documents]",
	)

	if err := skills.VerifyFrontmatter(entry, md); err != nil {
		t.Fatalf("VerifyFrontmatter: %v", err)
	}
}

func TestVerifyFrontmatter_NestedMismatchIsRejected(t *testing.T) {
	entry := entryWith(map[string]any{
		"name":        "demo",
		"description": "A demo skill",
		"metadata":    map[string]any{"version": "2.1.0"},
	})
	md := skillMD("name: demo", "description: A demo skill", "metadata:", "  version: 9.9.9")

	if err := skills.VerifyFrontmatter(entry, md); !errors.Is(err, skills.ErrFrontmatterMismatch) {
		t.Fatalf("err = %v, want ErrFrontmatterMismatch", err)
	}
}

func TestVerifyFrontmatter_UnparseableSkillMDIsRejected(t *testing.T) {
	entry := entryWith(map[string]any{"name": "demo", "description": "A demo skill"})

	if err := skills.VerifyFrontmatter(entry, []byte("no frontmatter here\n")); !errors.Is(err, skills.ErrFrontmatterMismatch) {
		t.Fatalf("err = %v, want ErrFrontmatterMismatch", err)
	}
}

// ReadFromEntry applies the comparison to the SKILL.md automatically, so a
// caller gets the SEP's MUST without opting in.
func TestReadFromEntry_VerifiesSkillMDFrontmatter(t *testing.T) {
	sc, _ := connectSkillsClient(t, "testdata/valid")

	entries, err := sc.ListSkillEntries(t.Context())
	if err != nil {
		t.Fatalf("ListSkillEntries: %v", err)
	}
	e := entryFor(t, entries, "pdf-processing")

	if _, err := sc.ReadFromEntry(t.Context(), e, e.URI); err != nil {
		t.Fatalf("honest server rejected: %v", err)
	}

	// Same bytes, same digest, tampered listing. Only the frontmatter
	// comparison can catch this.
	tampered := e
	fm := maps.Clone(e.Frontmatter)
	fm["description"] = "something the SKILL.md does not say"
	tampered.Frontmatter = fm

	_, err = sc.ReadFromEntry(t.Context(), tampered, tampered.URI)
	if !errors.Is(err, skills.ErrFrontmatterMismatch) {
		t.Fatalf("err = %v, want ErrFrontmatterMismatch", err)
	}
}
