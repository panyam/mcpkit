package skills_test

import (
	"encoding/json"
	"errors"
	"strings"
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
    },
    {
      "type": "mcp-resource-template",
      "description": "Per-product documentation skill",
      "url": "skill://docs/{product}/SKILL.md"
    }
  ]
}`

func TestIndex_UnmarshalSEPExample(t *testing.T) {
	var idx skills.Index
	if err := json.Unmarshal([]byte(sepExampleIndex), &idx); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if idx.Schema != skills.IndexSchemaURI {
		t.Errorf("Schema = %q, want %q", idx.Schema, skills.IndexSchemaURI)
	}
	if len(idx.Skills) != 4 {
		t.Fatalf("Skills count = %d, want 4", len(idx.Skills))
	}

	// Verify each entry's type-specific shape.
	want := []struct {
		typ    skills.SkillType
		name   string
		url    string
		hasDig bool
	}{
		{skills.SkillTypeSkillMD, "git-workflow", "skill://git-workflow/SKILL.md", true},
		{skills.SkillTypeSkillMD, "refunds", "skill://acme/billing/refunds/SKILL.md", true},
		{skills.SkillTypeArchive, "pdf-processing", "skill://pdf-processing.tar.gz", true},
		{skills.SkillTypeResourceTemplate, "", "skill://docs/{product}/SKILL.md", false},
	}
	for i, w := range want {
		got := idx.Skills[i]
		if got.Type != w.typ {
			t.Errorf("skills[%d].Type = %q, want %q", i, got.Type, w.typ)
		}
		if got.Name != w.name {
			t.Errorf("skills[%d].Name = %q, want %q", i, got.Name, w.name)
		}
		if got.URL != w.url {
			t.Errorf("skills[%d].URL = %q, want %q", i, got.URL, w.url)
		}
		if w.hasDig && !strings.HasPrefix(got.Digest, "sha256:") {
			t.Errorf("skills[%d].Digest = %q, want sha256:... prefix", i, got.Digest)
		}
		if !w.hasDig && got.Digest != "" {
			t.Errorf("skills[%d].Digest = %q, want empty for type %q", i, got.Digest, w.typ)
		}
	}
}

func TestIndexEntry_RoundTrip(t *testing.T) {
	// Each entry should round-trip JSON → struct → JSON with all fields
	// preserved, including the conditional name/digest behavior.
	var orig skills.Index
	if err := json.Unmarshal([]byte(sepExampleIndex), &orig); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	encoded, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var second skills.Index
	if err := json.Unmarshal(encoded, &second); err != nil {
		t.Fatalf("Re-unmarshal: %v", err)
	}
	if len(second.Skills) != len(orig.Skills) {
		t.Fatalf("round-trip lost entries: %d → %d", len(orig.Skills), len(second.Skills))
	}
	for i := range orig.Skills {
		if orig.Skills[i] != second.Skills[i] {
			t.Errorf("entry %d: round-trip mismatch\n  orig = %#v\n  new  = %#v", i, orig.Skills[i], second.Skills[i])
		}
	}

	// The mcp-resource-template entry must encode without name or digest
	// keys (omitempty in practice). Re-encode it individually to assert.
	tmpl := orig.Skills[3]
	if tmpl.Type != skills.SkillTypeResourceTemplate {
		t.Fatalf("test fixture drift: expected template at index 3, got %q", tmpl.Type)
	}
	tplBytes, err := json.Marshal(tmpl)
	if err != nil {
		t.Fatalf("Marshal template: %v", err)
	}
	if strings.Contains(string(tplBytes), `"name"`) {
		t.Errorf("template entry should omit name: %s", tplBytes)
	}
	if strings.Contains(string(tplBytes), `"digest"`) {
		t.Errorf("template entry should omit digest: %s", tplBytes)
	}
}

func TestIndexEntry_Validate(t *testing.T) {
	cases := []struct {
		name  string
		entry skills.IndexEntry
		want  error
	}{
		{
			name: "valid skill-md",
			entry: skills.IndexEntry{
				Type:        skills.SkillTypeSkillMD,
				Name:        "git-workflow",
				Description: "git stuff",
				URL:         "skill://git-workflow/SKILL.md",
				Digest:      "sha256:abc",
			},
		},
		{
			name: "valid archive",
			entry: skills.IndexEntry{
				Type:        skills.SkillTypeArchive,
				Name:        "pdf-processing",
				Description: "pdfs",
				URL:         "skill://pdf-processing.tar.gz",
				Digest:      "sha256:def",
			},
		},
		{
			name: "valid template",
			entry: skills.IndexEntry{
				Type:        skills.SkillTypeResourceTemplate,
				Description: "per-product docs",
				URL:         "skill://docs/{product}/SKILL.md",
			},
		},
		{
			name:  "unknown type",
			entry: skills.IndexEntry{Type: "garbage", Description: "x", URL: "y"},
			want:  skills.ErrUnknownSkillType,
		},
		{
			name:  "skill-md missing name",
			entry: skills.IndexEntry{Type: skills.SkillTypeSkillMD, Description: "x", URL: "y", Digest: "sha256:1"},
			want:  skills.ErrIndexEntryMissingName,
		},
		{
			name:  "skill-md missing digest",
			entry: skills.IndexEntry{Type: skills.SkillTypeSkillMD, Name: "n", Description: "x", URL: "y"},
			want:  skills.ErrIndexEntryMissingDigest,
		},
		{
			name:  "missing description",
			entry: skills.IndexEntry{Type: skills.SkillTypeSkillMD, Name: "n", URL: "y", Digest: "sha256:1"},
			want:  skills.ErrIndexEntryMissingDescription,
		},
		{
			name:  "missing url",
			entry: skills.IndexEntry{Type: skills.SkillTypeSkillMD, Name: "n", Description: "x", Digest: "sha256:1"},
			want:  skills.ErrIndexEntryMissingURL,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.entry.Validate()
			if tc.want == nil {
				if err != nil {
					t.Errorf("Validate err = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("Validate err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestIndex_Validate(t *testing.T) {
	idx := skills.NewIndex(skills.IndexEntry{
		Type:        skills.SkillTypeSkillMD,
		Name:        "git-workflow",
		Description: "x",
		URL:         "skill://git-workflow/SKILL.md",
		Digest:      "sha256:abc",
	})
	if err := idx.Validate(); err != nil {
		t.Errorf("Validate good index err = %v", err)
	}

	bad := skills.Index{} // missing schema
	if err := bad.Validate(); !errors.Is(err, skills.ErrIndexMissingSchema) {
		t.Errorf("Validate empty index err = %v, want ErrIndexMissingSchema", err)
	}
}

func TestSkillType(t *testing.T) {
	if !skills.SkillTypeSkillMD.Valid() {
		t.Error("skill-md should be valid")
	}
	if !skills.SkillTypeArchive.HasManifestFields() {
		t.Error("archive should have manifest fields")
	}
	if skills.SkillTypeResourceTemplate.HasManifestFields() {
		t.Error("template should not have manifest fields")
	}
	if skills.SkillType("nope").Valid() {
		t.Error("garbage value should not validate")
	}
}

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
