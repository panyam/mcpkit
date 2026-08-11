package files

import (
	"context"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/core"
)

func renderCall(name, args string) (string, bool) {
	return renderEditApproval(context.Background(), agent.ToolCallInfo{
		Call: agent.ToolCall{Name: name, Args: core.NewRawJSON([]byte(args))},
	})
}

// TestEditApprovalShowsTheDiff is the reason the seam exists. The default host
// prompt would render this call's JSON trimmed to 200 characters, which cuts
// off the change the user is being asked to approve.
func TestEditApprovalShowsTheDiff(t *testing.T) {
	got, ok := renderCall("edit_file", `{
		"path": "main.go",
		"expect_hash": "abc123",
		"edits": [{"old": "func Old() {}", "new": "func New() {}"}]
	}`)
	if !ok {
		t.Fatalf("edit_file should be claimed, got %q", got)
	}
	for _, want := range []string{"main.go", "1 change", "- func Old() {}", "+ func New() {}"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "expect_hash") {
		t.Errorf("the hash is bookkeeping, not something to review:\n%s", got)
	}
}

func TestEditApprovalCountsEveryHunk(t *testing.T) {
	got, ok := renderCall("edit_file", `{
		"path": "main.go",
		"edits": [
			{"old": "one", "new": "ONE"},
			{"old": "two", "new": "TWO"}
		]
	}`)
	if !ok {
		t.Fatal("edit_file should be claimed")
	}
	if !strings.Contains(got, "2 change(s)") {
		t.Errorf("prompt should count the hunks:\n%s", got)
	}
	for _, want := range []string{"- one", "+ ONE", "- two", "+ TWO"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt is missing %q:\n%s", want, got)
		}
	}
}

// TestDeletionRendersWithNoAddedLines keeps an empty `new` from printing a
// bare "+" that reads like an added blank line rather than a deletion.
func TestDeletionRendersWithNoAddedLines(t *testing.T) {
	got, ok := renderCall("edit_file", `{"path":"a.go","edits":[{"old":"gone","new":""}]}`)
	if !ok {
		t.Fatal("edit_file should be claimed")
	}
	if !strings.Contains(got, "- gone") {
		t.Errorf("prompt should show the removed line:\n%s", got)
	}
	if strings.Contains(got, "+") {
		t.Errorf("a deletion should add no lines:\n%s", got)
	}
}

func TestWriteApprovalDistinguishesCreateFromReplace(t *testing.T) {
	create, ok := renderCall("write_file", `{"path":"new.go","content":"package main\n"}`)
	if !ok {
		t.Fatal("write_file should be claimed")
	}
	if !strings.Contains(create, "Create new.go") {
		t.Errorf("a create should say so:\n%s", create)
	}

	replace, ok := renderCall("write_file", `{"path":"old.go","content":"package main\n","expect_hash":"abc"}`)
	if !ok {
		t.Fatal("write_file should be claimed")
	}
	if !strings.Contains(replace, "Replace old.go") {
		t.Errorf("a replace should say so, since it destroys content:\n%s", replace)
	}
}

// TestWriteApprovalCapsItsOwnPreview is the renderer exercising the freedom
// the seam gives it. The default's 200-character trim is wrong for a diff, but
// unbounded output is wrong for a whole-file write, so the renderer picks.
func TestWriteApprovalCapsItsOwnPreview(t *testing.T) {
	var body strings.Builder
	for i := range 200 {
		body.WriteString("line ")
		body.WriteByte(byte('0' + i%10))
		body.WriteString("\n")
	}
	got, ok := renderCall("write_file", `{"path":"big.go","content":`+jsonQuote(body.String())+`}`)
	if !ok {
		t.Fatal("write_file should be claimed")
	}
	if !strings.Contains(got, "more line(s)") {
		t.Errorf("a long write should be capped and say so:\n%s", got[:min(400, len(got))])
	}
	if n := strings.Count(got, "\n"); n > 60 {
		t.Errorf("preview is %d lines, want it bounded", n)
	}
}

// TestUnknownToolsAreDeclined keeps the default covering everything this
// package does not own. Claiming broadly would silently replace the prompt for
// tools whose arguments this renderer knows nothing about.
func TestUnknownToolsAreDeclined(t *testing.T) {
	for _, name := range []string{"read_file", "list_files", "search_files", "some_other_tool"} {
		if got, ok := renderCall(name, `{"path":"a.go"}`); ok {
			t.Errorf("%s should not be claimed, got %q", name, got)
		}
	}
}

// TestMalformedArgumentsDecline is where the generic rendering is the honest
// one: it shows the raw text rather than a summary built from a guess.
func TestMalformedArgumentsDecline(t *testing.T) {
	cases := map[string]string{
		"not json":      `{"path": `,
		"no path":       `{"edits":[{"old":"a","new":"b"}]}`,
		"no edits":      `{"path":"a.go"}`,
		"empty edits":   `{"path":"a.go","edits":[]}`,
		"bad edit item": `{"path":"a.go","edits":["nope"]}`,
		"no content":    `{"path":"a.go"}`,
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if got, ok := renderCall("edit_file", args); ok {
				t.Errorf("should have declined, got %q", got)
			}
		})
	}
}

// TestRendererIsRegistered pins the wiring, since a renderer that is never
// collected is a diff nobody sees.
func TestRendererIsRegistered(t *testing.T) {
	e, err := New(Config{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	rs := e.ApprovalRenderers()
	if len(rs) != 1 {
		t.Fatalf("got %d renderers, want 1", len(rs))
	}
	got, ok := rs[0](context.Background(), agent.ToolCallInfo{
		Call: agent.ToolCall{Name: "edit_file", Args: core.NewRawJSON([]byte(`{"path":"a.go","edits":[{"old":"x","new":"y"}]}`))},
	})
	if !ok || !strings.Contains(got, "a.go") {
		t.Errorf("registered renderer did not render: %q", got)
	}
}

func jsonQuote(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `"`, `\"`), "\n", `\n`) + `"`
}
