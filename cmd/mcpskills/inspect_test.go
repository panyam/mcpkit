package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/skills"
	"github.com/panyam/mcpkit/server"
)

// startSkillsServer boots an in-process MCP server that publishes the
// named skills directory and returns its URL. Mirrors the pattern used
// in ext/skills/client_test.go.
func startSkillsServer(t *testing.T, dir string) string {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "mcpskills-inspect-test", Version: "0.0.1"})
	p, err := skills.NewProvider(skills.WithDirectory(dir))
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	p.RegisterWith(srv)

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts.URL + "/mcp"
}

// TestInspectCmd_JSON connects to a Provider-backed server and asserts
// the JSON report carries every skill with a verified digest.
func TestInspectCmd_JSON(t *testing.T) {
	url := startSkillsServer(t, "../../ext/skills/testdata/valid")

	out := &bytes.Buffer{}
	root := newRoot()
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"inspect", url, "--json", "--color", "never"})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v\noutput:\n%s", err, out.String())
	}

	var report inspectReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("unmarshal report: %v\nraw:\n%s", err, out.String())
	}
	if !report.CapabilityDeclared {
		t.Errorf("capability not declared")
	}
	if len(report.Entries) != 3 {
		t.Errorf("entries = %d, want 3", len(report.Entries))
	}
	if report.HasFailures {
		t.Errorf("HasFailures = true, want false (all digests should verify)")
	}
	for _, e := range report.Entries {
		if e.Verified == nil || !*e.Verified {
			t.Errorf("entry %q: verified = %v, want true", e.Name, e.Verified)
		}
		if e.Files < 1 {
			t.Errorf("entry %q: files = %d, want at least the SKILL.md", e.Name, e.Files)
		}
		if !strings.HasPrefix(e.Digest, "sha256:") {
			t.Errorf("entry %q: digest = %q, want the SKILL.md's sha256 pin", e.Name, e.Digest)
		}
	}
}

// TestInspectCmd_Text exercises the human-readable path through
// renderText. We only assert structure markers so this stays robust
// against cosmetic changes.
func TestInspectCmd_Text(t *testing.T) {
	url := startSkillsServer(t, "../../ext/skills/testdata/valid")

	out := &bytes.Buffer{}
	root := newRoot()
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"inspect", url, "--color", "never"})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v\noutput:\n%s", err, out.String())
	}
	got := out.String()
	for _, want := range []string{
		"io.modelcontextprotocol/skills declared",
		"skills/list: 3 skills",
		"digest verified",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("inspect output missing %q\nfull output:\n%s", want, got)
		}
	}
}

// TestInspectCmd_PlainServer asserts that a server which does not
// advertise the io.modelcontextprotocol/skills capability is still
// reachable and reports CapabilityDeclared=false rather than erroring.
func TestInspectCmd_PlainServer(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "plain", Version: "0.0.1"})
	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	out := &bytes.Buffer{}
	root := newRoot()
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"inspect", ts.URL + "/mcp", "--json", "--color", "never"})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v\noutput:\n%s", err, out.String())
	}
	var report inspectReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if report.CapabilityDeclared {
		t.Errorf("CapabilityDeclared = true on a server without the extension")
	}
	if len(report.Entries) != 0 {
		t.Errorf("entries = %d, want 0 on a server without the extension", len(report.Entries))
	}
}

// TestInspectCmd_TamperedFile edits a supporting file after the Provider
// has pinned its digest, so skills/list promises bytes the server no
// longer serves. inspect must flag that skill and exit non-zero.
func TestInspectCmd_TamperedFile(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "tamper-me")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "---\nname: tamper-me\ndescription: Fixture whose supporting file changes after pinning\n---\n\nBody.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	notePath := filepath.Join(skillDir, "note.md")
	if err := os.WriteFile(notePath, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	url := startSkillsServer(t, dir)
	if err := os.WriteFile(notePath, []byte("tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := &bytes.Buffer{}
	root := newRoot()
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"inspect", url, "--json", "--color", "never"})

	if err := root.Execute(); err == nil {
		t.Fatalf("Execute: want an error for a tampered file, got nil\noutput:\n%s", out.String())
	}
	var report inspectReport
	if err := json.NewDecoder(out).Decode(&report); err != nil {
		t.Fatalf("decode report: %v\nraw:\n%s", err, out.String())
	}
	if !report.HasFailures {
		t.Errorf("HasFailures = false, want true")
	}
	if len(report.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(report.Entries))
	}
	e := report.Entries[0]
	if e.Verified == nil || *e.Verified {
		t.Errorf("verified = %v, want false", e.Verified)
	}
	if !strings.Contains(e.Error, "note.md") {
		t.Errorf("error = %q, want it to name the tampered file", e.Error)
	}
}
