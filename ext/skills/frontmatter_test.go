package skills_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/ext/skills"
)

// gitWorkflowSkillMD is the sample shape used by SEP-2640 examples and the
// experimental-ext-skills reference repo: minimal name + description plus
// a markdown body.
const gitWorkflowSkillMD = `---
name: git-workflow
description: Follow this team's Git conventions for branching and commits
---

# Git Workflow

Use feature branches off main.
`

func TestParseFrontmatter_GoodCase(t *testing.T) {
	fm, body, err := skills.ParseFrontmatter([]byte(gitWorkflowSkillMD))
	if err != nil {
		t.Fatalf("ParseFrontmatter: %v", err)
	}
	if fm.Name != "git-workflow" {
		t.Errorf("Name = %q", fm.Name)
	}
	if fm.Description != "Follow this team's Git conventions for branching and commits" {
		t.Errorf("Description = %q", fm.Description)
	}
	if len(fm.Extra) != 0 {
		t.Errorf("Extra = %v, want empty", fm.Extra)
	}
	if !strings.HasPrefix(string(body), "# Git Workflow") {
		t.Errorf("body should start with the H1, got %q", string(body))
	}
}

func TestParseFrontmatter_BOM(t *testing.T) {
	withBOM := append([]byte{0xEF, 0xBB, 0xBF}, []byte(gitWorkflowSkillMD)...)
	fm, _, err := skills.ParseFrontmatter(withBOM)
	if err != nil {
		t.Fatalf("ParseFrontmatter with BOM: %v", err)
	}
	if fm.Name != "git-workflow" {
		t.Errorf("Name = %q", fm.Name)
	}
}

func TestParseFrontmatter_CRLF(t *testing.T) {
	crlf := strings.ReplaceAll(gitWorkflowSkillMD, "\n", "\r\n")
	fm, body, err := skills.ParseFrontmatter([]byte(crlf))
	if err != nil {
		t.Fatalf("ParseFrontmatter CRLF: %v", err)
	}
	if fm.Name != "git-workflow" {
		t.Errorf("Name = %q", fm.Name)
	}
	if strings.Contains(string(body), "\r") {
		t.Errorf("body still contains CR characters")
	}
}

func TestParseFrontmatter_CRonly(t *testing.T) {
	crOnly := strings.ReplaceAll(gitWorkflowSkillMD, "\n", "\r")
	fm, _, err := skills.ParseFrontmatter([]byte(crOnly))
	if err != nil {
		t.Fatalf("ParseFrontmatter CR-only: %v", err)
	}
	if fm.Name != "git-workflow" {
		t.Errorf("Name = %q", fm.Name)
	}
}

func TestParseFrontmatter_ExtraFields(t *testing.T) {
	src := `---
name: refunds
description: Process refund requests
version: 1.2
tags:
  - billing
  - finance
allowed-tools:
  - mcp__crm__create_refund
---

body
`
	fm, body, err := skills.ParseFrontmatter([]byte(src))
	if err != nil {
		t.Fatalf("ParseFrontmatter: %v", err)
	}
	if fm.Name != "refunds" {
		t.Errorf("Name = %q", fm.Name)
	}
	if fm.Description != "Process refund requests" {
		t.Errorf("Description = %q", fm.Description)
	}
	if v, ok := fm.Extra["version"]; !ok || v != 1.2 {
		t.Errorf("Extra[version] = %v (%T)", v, v)
	}
	if v, ok := fm.Extra["tags"].([]any); !ok || len(v) != 2 || v[0] != "billing" {
		t.Errorf("Extra[tags] = %v", fm.Extra["tags"])
	}
	if _, ok := fm.Extra["allowed-tools"]; !ok {
		t.Errorf("Extra missing allowed-tools")
	}
	if !strings.HasPrefix(string(body), "body") {
		t.Errorf("body = %q", string(body))
	}
}

func TestParseFrontmatter_EmptyBody(t *testing.T) {
	src := `---
name: empty
description: This skill has no body
---
`
	fm, body, err := skills.ParseFrontmatter([]byte(src))
	if err != nil {
		t.Fatalf("ParseFrontmatter: %v", err)
	}
	if fm.Name != "empty" {
		t.Errorf("Name = %q", fm.Name)
	}
	if len(body) != 0 {
		t.Errorf("body = %q, want empty", string(body))
	}
}

func TestParseFrontmatter_Errors(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want error
	}{
		{
			name: "missing opening fence",
			src:  "# No frontmatter here\n",
			want: skills.ErrMissingFrontmatter,
		},
		{
			name: "empty input",
			src:  "",
			want: skills.ErrMissingFrontmatter,
		},
		{
			name: "unterminated frontmatter",
			src:  "---\nname: x\ndescription: y\n# body without closing fence\n",
			want: skills.ErrUnterminatedFrontmatter,
		},
		{
			name: "list frontmatter",
			src:  "---\n- a\n- b\n---\nbody\n",
			want: skills.ErrNonMappingFrontmatter,
		},
		{
			name: "scalar frontmatter",
			src:  "---\nfoo\n---\nbody\n",
			want: skills.ErrNonMappingFrontmatter,
		},
		{
			name: "missing name",
			src:  "---\ndescription: x\n---\nbody\n",
			want: skills.ErrFrontmatterMissingName,
		},
		{
			name: "missing description",
			src:  "---\nname: x\n---\nbody\n",
			want: skills.ErrFrontmatterMissingDescription,
		},
		{
			name: "empty name",
			src:  "---\nname: ''\ndescription: x\n---\nbody\n",
			want: skills.ErrFrontmatterMissingName,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := skills.ParseFrontmatter([]byte(tc.src))
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestParseFrontmatterReader(t *testing.T) {
	fm, body, err := skills.ParseFrontmatterReader(strings.NewReader(gitWorkflowSkillMD))
	if err != nil {
		t.Fatalf("ParseFrontmatterReader: %v", err)
	}
	if fm.Name != "git-workflow" {
		t.Errorf("Name = %q", fm.Name)
	}
	if len(body) == 0 {
		t.Errorf("body should not be empty")
	}
}

func TestParseFrontmatter_NullFrontmatter(t *testing.T) {
	// An "empty" frontmatter block ("---\n---\n") decodes to a YAML null,
	// which is not a mapping.
	src := "---\n---\nbody\n"
	_, _, err := skills.ParseFrontmatter([]byte(src))
	if !errors.Is(err, skills.ErrNonMappingFrontmatter) {
		t.Errorf("err = %v, want ErrNonMappingFrontmatter", err)
	}
}

func TestParseFrontmatter_DelimiterWithTrailingSpaces(t *testing.T) {
	src := "---  \nname: x\ndescription: y\n---\t\nbody\n"
	fm, _, err := skills.ParseFrontmatter([]byte(src))
	if err != nil {
		t.Fatalf("ParseFrontmatter: %v", err)
	}
	if fm.Name != "x" {
		t.Errorf("Name = %q", fm.Name)
	}
}
