package skills_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/ext/skills"
)

// callSkillsMethod issues a skills/* method and unmarshals into out. Skills
// methods are JSON-RPC calls rather than resource reads, so the readResource
// helpers do not apply.
func callSkillsMethod(t *testing.T, c *client.Client, method string, params any, out any) {
	t.Helper()
	res, err := c.Call(t.Context(), method, params)
	if err != nil {
		t.Fatalf("Call(%s): %v", method, err)
	}
	if err := res.Unmarshal(out); err != nil {
		t.Fatalf("Unmarshal(%s): %v", method, err)
	}
}

// TestSkillsList_EntryShape pins the wire shape against bytes captured from
// hf-mcp-server, the reference implementation: every entry carries uri,
// frontmatter and resources, and resources lists the skill's own SKILL.md.
func TestSkillsList_EntryShape(t *testing.T) {
	_, _, c := boot(t, "testdata/valid")

	var res skills.SkillsListResult
	callSkillsMethod(t, c, skills.MethodSkillsList, skills.SkillsListRequest{}, &res)
	if len(res.Skills) == 0 {
		t.Fatal("skills/list returned no entries")
	}

	for _, e := range res.Skills {
		if e.URI == "" {
			t.Error("entry uri is empty")
		}
		if e.Frontmatter == nil {
			t.Errorf("%s: frontmatter is nil", e.URI)
			continue
		}
		if e.Name() == "" || e.Description() == "" {
			t.Errorf("%s: frontmatter missing name or description", e.URI)
		}
		if e.Resources.Dynamic {
			t.Errorf("%s: static skill reported the dynamic sentinel", e.URI)
			continue
		}
		if len(e.Resources.Files) == 0 {
			t.Errorf("%s: resources is empty", e.URI)
			continue
		}
		var sawSelf bool
		for _, f := range e.Resources.Files {
			if f.URI == e.URI {
				sawSelf = true
			}
			if f.Digest == "" {
				t.Errorf("%s: resource %s has no digest", e.URI, f.URI)
			}
			if f.Size <= 0 {
				t.Errorf("%s: resource %s has size %d, want > 0", e.URI, f.URI, f.Size)
			}
		}
		if !sawSelf {
			t.Errorf("%s: resources omits the skill's own SKILL.md; the manifest must be complete", e.URI)
		}
	}
}

// TestSkillsList_FrontmatterVerbatim covers the field-by-field comparison the
// SEP requires of hosts: keys beyond name and description must survive, or a
// host re-verifying a fetched SKILL.md against the entry would see a
// mismatch. pdf-processing carries version and tags.
func TestSkillsList_FrontmatterVerbatim(t *testing.T) {
	_, _, c := boot(t, "testdata/valid")

	var res skills.SkillsListResult
	callSkillsMethod(t, c, skills.MethodSkillsList, skills.SkillsListRequest{}, &res)

	var found bool
	for _, e := range res.Skills {
		if e.Name() != "pdf-processing" {
			continue
		}
		found = true
		if got, ok := e.Frontmatter["version"]; !ok || got != "0.2.0" {
			t.Errorf("frontmatter version = %v, want 0.2.0; extra keys must be carried verbatim", got)
		}
		if _, ok := e.Frontmatter["tags"]; !ok {
			t.Error("frontmatter tags missing; list-valued keys must be carried verbatim")
		}
	}
	if !found {
		t.Fatal("pdf-processing not in listing")
	}
}

// TestSkillsGet_ReturnsWrappedEntry pins the result shape hf-mcp-server
// emits: the entry sits under a "skill" key, and no cursor is present because
// a single entry is not a list.
func TestSkillsGet_ReturnsWrappedEntry(t *testing.T) {
	_, _, c := boot(t, "testdata/valid")

	var lr skills.SkillsListResult
	callSkillsMethod(t, c, skills.MethodSkillsList, skills.SkillsListRequest{}, &lr)
	want := lr.Skills[0]

	var generic map[string]json.RawMessage
	callSkillsMethod(t, c, skills.MethodSkillsGet, skills.SkillsGetRequest{URI: want.URI}, &generic)
	if _, ok := generic["skill"]; !ok {
		t.Errorf("result has no \"skill\" key; got %v", genericKeys(generic))
	}
	if _, ok := generic["nextCursor"]; ok {
		t.Error("skills/get carried nextCursor; a single entry is not a list")
	}

	var res skills.SkillsGetResult
	callSkillsMethod(t, c, skills.MethodSkillsGet, skills.SkillsGetRequest{URI: want.URI}, &res)
	if res.Skill.URI != want.URI {
		t.Errorf("skill.uri = %q, want %q", res.Skill.URI, want.URI)
	}
	if len(res.Skill.Resources.Files) != len(want.Resources.Files) {
		t.Errorf("skills/get resources = %d entries, skills/list = %d; the shapes must be identical",
			len(res.Skill.Resources.Files), len(want.Resources.Files))
	}
}

// TestSkillsGet_UnknownURIIsInvalidParams pins the error code. hf-mcp-server
// answers -32602 for an unserved URI, matching resources/read for an unknown
// resource.
func TestSkillsGet_UnknownURIIsInvalidParams(t *testing.T) {
	_, _, c := boot(t, "testdata/valid")

	_, err := c.Call(t.Context(), skills.MethodSkillsGet, skills.SkillsGetRequest{URI: "skill://not-served/SKILL.md"})
	if err == nil {
		t.Fatal("skills/get on an unknown URI succeeded, want -32602")
	}
	if !strings.Contains(err.Error(), "-32602") {
		t.Errorf("error = %v, want -32602 (Invalid params)", err)
	}
}

func genericKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
