package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestVerifyCmd_Valid runs `mcpskills verify` against the ext/skills
// valid testdata directory and asserts every skill is reported
// passing. Exercises the happy path through findSkillDirs and
// verifySkill.
func TestVerifyCmd_Valid(t *testing.T) {
	out := &bytes.Buffer{}
	root := newRoot()
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"verify", "../../ext/skills/testdata/valid", "--color", "never"})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v\noutput:\n%s", err, out.String())
	}
	got := out.String()
	for _, want := range []string{
		"acme/billing/refunds",
		"git-workflow",
		"pdf-processing",
		"3 skills, 0 errors",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("verify output missing %q\nfull output:\n%s", want, got)
		}
	}
}

// TestVerifyCmd_NestedSkill walks the bad-nested fixture where one
// skill contains another skill below it. verify must flag the nested
// SKILL.md and still pass the surrounding outer skill.
func TestVerifyCmd_NestedSkill(t *testing.T) {
	out := &bytes.Buffer{}
	root := newRoot()
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"verify", "../../ext/skills/testdata/bad-nested", "--color", "never"})

	err := root.Execute()
	if err == nil {
		t.Fatalf("Execute: want non-nil error, got nil\noutput:\n%s", out.String())
	}
	got := out.String()
	if !strings.Contains(got, "nested skill") {
		t.Errorf("verify output missing nested-skill marker\nfull output:\n%s", got)
	}
	if !strings.Contains(got, "1 error") {
		t.Errorf("verify summary missing error count\nfull output:\n%s", got)
	}
}

// TestVerifyCmd_NameMismatch walks the bad-name-mismatch fixture
// where the frontmatter name disagrees with the directory name.
func TestVerifyCmd_NameMismatch(t *testing.T) {
	out := &bytes.Buffer{}
	root := newRoot()
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"verify", "../../ext/skills/testdata/bad-name-mismatch", "--color", "never"})

	err := root.Execute()
	if err == nil {
		t.Fatalf("Execute: want non-nil error, got nil\noutput:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "frontmatter name does not match directory") {
		t.Errorf("verify output missing name-mismatch surface\nfull output:\n%s", out.String())
	}
}

// TestVerifyCmd_MissingDir asserts the command surfaces an os.Stat
// error rather than panicking when the target directory doesn't
// exist.
func TestVerifyCmd_MissingDir(t *testing.T) {
	out := &bytes.Buffer{}
	root := newRoot()
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"verify", "/nonexistent/path/that/cannot/exist", "--color", "never"})

	if err := root.Execute(); err == nil {
		t.Fatalf("Execute: want non-nil error for missing dir, got nil")
	}
}
