package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
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
		"skill://index.json",
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
