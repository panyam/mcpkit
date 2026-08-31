package skills_test

import (
	"testing"

	"github.com/panyam/mcpkit/ext/skills"
)

const (
	outerURI = "skill://outer/SKILL.md"
	innerURI = "skill://outer/inner/SKILL.md"
)

// TestNested_PublicationIsFlat covers the first of the three nesting rules:
// a nested skill gets its own listing entry whose URI merely shares a path
// prefix with its parent's. Nothing in the listing marks the relationship.
func TestNested_PublicationIsFlat(t *testing.T) {
	sc, _ := connectSkillsClient(t, "testdata/nested")

	entries, err := sc.ListSkillEntries(t.Context())
	if err != nil {
		t.Fatalf("ListSkillEntries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entry count = %d, want 2 (outer and inner are both skills)", len(entries))
	}

	byURI := map[string]skills.SkillEntry{}
	for _, e := range entries {
		byURI[e.URI] = e
	}
	outer, ok := byURI[outerURI]
	if !ok {
		t.Fatalf("outer missing; got %v", byURI)
	}
	inner, ok := byURI[innerURI]
	if !ok {
		t.Fatalf("nested skill missing from the listing; got %v", byURI)
	}
	if outer.Name() != "outer" || inner.Name() != "inner" {
		t.Errorf("names = %q / %q, want outer / inner", outer.Name(), inner.Name())
	}
}

// TestNested_EnclosingResourcesIncludeNestedFiles covers the completeness
// rule: from the enclosing skill's perspective a nested skill's directory is
// ordinary supporting content, so its files appear in the enclosing entry's
// manifest as well as in the nested skill's own.
func TestNested_EnclosingResourcesIncludeNestedFiles(t *testing.T) {
	sc, _ := connectSkillsClient(t, "testdata/nested")

	outer, err := sc.GetSkill(t.Context(), outerURI)
	if err != nil {
		t.Fatalf("GetSkill(outer): %v", err)
	}
	inner, err := sc.GetSkill(t.Context(), innerURI)
	if err != nil {
		t.Fatalf("GetSkill(inner): %v", err)
	}

	outerURIs := map[string]bool{}
	for _, f := range outer.Resources.Files {
		outerURIs[f.URI] = true
	}
	for _, want := range []string{
		outerURI,
		"skill://outer/guide.md",
		innerURI,
		"skill://outer/inner/refs/note.md",
	} {
		if !outerURIs[want] {
			t.Errorf("outer manifest missing %s; a manifest is complete over the whole subtree", want)
		}
	}

	// The nested skill's manifest covers only its own subtree.
	for _, f := range inner.Resources.Files {
		if f.URI == outerURI || f.URI == "skill://outer/guide.md" {
			t.Errorf("inner manifest carries %s, which is outside the nested skill", f.URI)
		}
	}
	innerURIs := map[string]bool{}
	for _, f := range inner.Resources.Files {
		innerURIs[f.URI] = true
	}
	for _, want := range []string{innerURI, "skill://outer/inner/refs/note.md"} {
		if !innerURIs[want] {
			t.Errorf("inner manifest missing %s", want)
		}
	}
}

// TestNested_SharedFileServedOnce pins the resource side of the same rule.
// A file appears in two manifests but is one resource at one URI, so it is
// registered once and reads identically whichever entry pins it.
func TestNested_SharedFileServedOnce(t *testing.T) {
	sc, c := connectSkillsClient(t, "testdata/nested")

	defs, err := c.ListResources(t.Context())
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	seen := map[string]int{}
	for _, d := range defs {
		seen[d.URI]++
	}
	for uri, n := range seen {
		if n != 1 {
			t.Errorf("resource %s registered %d times, want 1", uri, n)
		}
	}
	if seen[innerURI] != 1 {
		t.Errorf("nested SKILL.md not served as a resource (count=%d)", seen[innerURI])
	}

	outer, err := sc.GetSkill(t.Context(), outerURI)
	if err != nil {
		t.Fatalf("GetSkill(outer): %v", err)
	}
	inner, err := sc.GetSkill(t.Context(), innerURI)
	if err != nil {
		t.Fatalf("GetSkill(inner): %v", err)
	}

	// The same bytes verify against either manifest's pin for the file.
	viaOuter, err := sc.ReadFromEntry(t.Context(), outer, innerURI)
	if err != nil {
		t.Fatalf("ReadFromEntry(outer, nested SKILL.md): %v", err)
	}
	viaInner, err := sc.ReadFromEntry(t.Context(), inner, innerURI)
	if err != nil {
		t.Fatalf("ReadFromEntry(inner, its own SKILL.md): %v", err)
	}
	if string(viaOuter.Bytes) != string(viaInner.Bytes) {
		t.Error("the same URI served different bytes to the two entries")
	}
	if !viaOuter.DigestVerified || !viaInner.DigestVerified {
		t.Error("both reads must verify")
	}
}

// TestNested_ReadThroughEnclosingIsSupportingContent covers the rule that a
// nested SKILL.md read as part of the enclosing skill is ordinary markdown:
// the host must not act on its frontmatter. ParseURI carries that distinction
// through IsManifest, which is false once the boundary is the enclosing skill.
func TestNested_ReadThroughEnclosingIsSupportingContent(t *testing.T) {
	parts, err := skills.ParseURI(innerURI)
	if err != nil {
		t.Fatalf("ParseURI: %v", err)
	}

	// Addressed directly, the nested SKILL.md is its own skill's manifest.
	if !parts.IsManifest {
		t.Error("addressed directly, a nested SKILL.md is a manifest")
	}
	if parts.SkillName != "inner" {
		t.Errorf("SkillName = %q, want inner", parts.SkillName)
	}

	// Split at the enclosing skill's boundary, it is a supporting file.
	viaOuter, err := parts.SplitAt(1)
	if err != nil {
		t.Fatalf("SplitAt(1): %v", err)
	}
	if viaOuter.IsManifest {
		t.Error("IsManifest = true below the enclosing boundary; the host would act on frontmatter it must ignore")
	}
	if viaOuter.SkillName != "outer" {
		t.Errorf("SkillName = %q, want outer", viaOuter.SkillName)
	}
}
