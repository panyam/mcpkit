package skills_test

import (
	"testing"

	"github.com/panyam/mcpkit/ext/skills"
)

// sepExampleIndex is the JSON example from SEP-2640's "Enumeration via
// skill://index.json" section. Round-tripping it ensures our struct tags,
// type discriminator, and omitempty rules match the spec example exactly.
const sepExampleIndex = `{
  "$schema": "https://schemas.agentskills.io/discovery/0.2.0/schema.json",
  "skills": [
    {
      "name": "git-workflow",
      "type": "skill-md",
      "description": "Follow this team's Git conventions for branching and commits",
      "url": "skill://git-workflow/SKILL.md",
      "digest": "sha256:a1b2c3d4..."
    },
    {
      "name": "refunds",
      "type": "skill-md",
      "description": "Process customer refund requests per company policy",
      "url": "skill://acme/billing/refunds/SKILL.md",
      "digest": "sha256:b2c3d4e5..."
    },
    {
      "name": "pdf-processing",
      "type": "archive",
      "description": "Extract, fill, and assemble PDF documents",
      "url": "skill://pdf-processing.tar.gz",
      "digest": "sha256:c4d5e6f7..."
    }
  ]
}`

func TestFrontmatter_Get(t *testing.T) {
	fm := skills.Frontmatter{
		Name:        "git-workflow",
		Description: "follow git",
		Extra:       map[string]any{"version": "1.0", "tags": []string{"git"}},
	}
	v, ok := fm.Get("name")
	if !ok || v != "git-workflow" {
		t.Errorf("Get(name) = %v, %v", v, ok)
	}
	v, ok = fm.Get("description")
	if !ok || v != "follow git" {
		t.Errorf("Get(description) = %v, %v", v, ok)
	}
	v, ok = fm.Get("version")
	if !ok || v != "1.0" {
		t.Errorf("Get(version) = %v, %v", v, ok)
	}
	_, ok = fm.Get("missing")
	if ok {
		t.Errorf("Get(missing) should report not found")
	}
}

func TestMetadataFromFrontmatter(t *testing.T) {
	fm := skills.Frontmatter{Name: "n", Description: "d", Extra: map[string]any{"x": 1}}
	m := skills.MetadataFromFrontmatter(fm, "skill://x/SKILL.md")
	if m.Name != "n" || m.Description != "d" {
		t.Errorf("Metadata copy lost fields: %#v", m)
	}
	if m.SourceURI != "skill://x/SKILL.md" {
		t.Errorf("SourceURI = %q", m.SourceURI)
	}
	if m.Extra["x"] != 1 {
		t.Errorf("Extra not copied: %#v", m.Extra)
	}
}
