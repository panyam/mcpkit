package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/agent"
)

// pathArg is the WriteSpec reader for a tool that names its target in "path",
// which is the shape files.PathArg has. Spelled locally so these tests do not
// depend on that module.
func pathArg(args map[string]any) []string {
	if p, ok := args["path"].(string); ok && p != "" {
		return []string{p}
	}
	return nil
}

func newStubExtension(t *testing.T, root string, s stubScript, writes ...WriteSpec) *Extension {
	t.Helper()
	ext, err := New(Config{
		Roots:              []string{root},
		Servers:            []ServerSpec{stubSpec(t, s)},
		Writes:             writes,
		DiagnosticsTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = ext.Close() })
	return ext
}

func callInfo(name, path string) agent.ToolCallInfo {
	raw, _ := json.Marshal(map[string]any{"path": path})
	return agent.ToolCallInfo{Call: agent.ToolCall{
		ID:   "c1",
		Name: name,
		Args: core.NewRawJSON(raw),
	}}
}

func okResult(text string) agent.ToolCallFunc {
	return func(context.Context, agent.ToolCallInfo) (*core.ToolResult, error) {
		return &core.ToolResult{Content: []core.Content{{Type: "text", Text: text}}}, nil
	}
}

func resultText(res *core.ToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		b.WriteString(c.Text)
		b.WriteString("\n")
	}
	return b.String()
}

// TestMiddlewareAppendsDiagnosticsAfterWrite is the within-turn half of the
// design. A context stage runs once per turn, so a model that edits at step 3
// and keeps working would learn nothing until the turn ended; the tool result
// is the only path that reaches it inside one.
func TestMiddlewareAppendsDiagnosticsAfterWrite(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n"})
	ext := newStubExtension(t, root, stubScript{Diagnostics: map[string][]diagnostic{
		"a.go": {{Range: textRange{Start: position{Line: 2, Character: 5}}, Severity: severityError, Message: "undefined: foo"}},
	}}, WriteSpec{Tool: "edit_file", Paths: pathArg})

	mw := ext.Middleware()
	if len(mw) != 1 {
		t.Fatalf("got %d middleware, want 1", len(mw))
	}
	res, err := mw[0](context.Background(), callInfo("edit_file", "a.go"), okResult("edited"))
	if err != nil {
		t.Fatalf("middleware: %v", err)
	}
	got := resultText(res)
	if !strings.Contains(got, "edited") {
		t.Fatalf("the tool's own result was lost: %q", got)
	}
	if !strings.Contains(got, "undefined: foo") || !strings.Contains(got, "3:6") {
		t.Fatalf("diagnostics not appended, or positions not 1-based: %q", got)
	}
}

// TestMiddlewareReportsACleanFile pins that success is stated rather than
// implied. "No problems" is the signal that closes the edit-check-fix loop, and
// it stays true as a statement about what this edit did.
func TestMiddlewareReportsACleanFile(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n"})
	ext := newStubExtension(t, root, stubScript{}, WriteSpec{Tool: "edit_file", Paths: pathArg})

	res, err := ext.Middleware()[0](context.Background(), callInfo("edit_file", "a.go"), okResult("edited"))
	if err != nil {
		t.Fatalf("middleware: %v", err)
	}
	if !strings.Contains(resultText(res), "no problems reported") {
		t.Fatalf("want an explicit all-clear, got %q", resultText(res))
	}
}

func TestMiddlewareIgnoresUnwatchedTools(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n"})
	ext := newStubExtension(t, root, stubScript{Diagnostics: map[string][]diagnostic{
		"a.go": {{Message: "undefined: foo"}},
	}}, WriteSpec{Tool: "edit_file", Paths: pathArg})

	res, err := ext.Middleware()[0](context.Background(), callInfo("read_file", "a.go"), okResult("contents"))
	if err != nil {
		t.Fatalf("middleware: %v", err)
	}
	if strings.Contains(resultText(res), "undefined: foo") {
		t.Fatalf("a read was treated as a write: %q", resultText(res))
	}
}

// TestMiddlewareSkipsACallThatFailed pins that nothing is re-checked when
// nothing was written. A denied or failed edit leaves the file alone, so
// diagnostics about it would describe the state before the call.
func TestMiddlewareSkipsACallThatFailed(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n"})
	ext := newStubExtension(t, root, stubScript{Diagnostics: map[string][]diagnostic{
		"a.go": {{Message: "undefined: foo"}},
	}}, WriteSpec{Tool: "edit_file", Paths: pathArg})

	failed := func(context.Context, agent.ToolCallInfo) (*core.ToolResult, error) {
		return &core.ToolResult{Content: []core.Content{{Type: "text", Text: "anchor not found"}}, IsError: true}, nil
	}
	res, err := ext.Middleware()[0](context.Background(), callInfo("edit_file", "a.go"), failed)
	if err != nil {
		t.Fatalf("middleware: %v", err)
	}
	if strings.Contains(resultText(res), "undefined: foo") {
		t.Fatalf("a failed edit was re-checked: %q", resultText(res))
	}

	dispatchErr := func(context.Context, agent.ToolCallInfo) (*core.ToolResult, error) {
		return nil, errors.New("transport died")
	}
	if _, err := ext.Middleware()[0](context.Background(), callInfo("edit_file", "a.go"), dispatchErr); err == nil {
		t.Fatal("a dispatch error must pass through")
	}
}

// TestMiddlewareReportsATimeoutRatherThanStaleDiagnostics pins the honest
// failure. Silence would read as "no problems" and old diagnostics would read
// as problems this edit caused.
func TestMiddlewareReportsATimeoutRatherThanStaleDiagnostics(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n"})
	ext, err := New(Config{
		Roots:              []string{root},
		Servers:            []ServerSpec{stubSpec(t, stubScript{NoPublish: true})},
		Writes:             []WriteSpec{{Tool: "edit_file", Paths: pathArg}},
		DiagnosticsTimeout: 150 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = ext.Close() })

	res, mwErr := ext.Middleware()[0](context.Background(), callInfo("edit_file", "a.go"), okResult("edited"))
	if mwErr != nil {
		t.Fatalf("middleware: %v", mwErr)
	}
	got := resultText(res)
	if !strings.Contains(got, "did not report back") {
		t.Fatalf("want the timeout stated, got %q", got)
	}
	if strings.Contains(got, "no problems reported") {
		t.Fatalf("a timeout was reported as an all-clear: %q", got)
	}
}

// TestStageInjectsCurrentDiagnosticsBeforeTheUser is the across-turn half. The
// block is the live picture, which is why it goes in the transient phase and
// never into history: "the file has these errors" stops being true the moment
// one is fixed.
func TestStageInjectsCurrentDiagnosticsBeforeTheUser(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n"})
	ext := newStubExtension(t, root, stubScript{Diagnostics: map[string][]diagnostic{
		"a.go": {{Range: textRange{Start: position{Line: 4}}, Severity: severityWarning, Message: "unused variable x"}},
	}}, WriteSpec{Tool: "edit_file", Paths: pathArg})

	if _, err := ext.Middleware()[0](context.Background(), callInfo("edit_file", "a.go"), okResult("edited")); err != nil {
		t.Fatalf("middleware: %v", err)
	}

	stages := ext.ContextStages()
	if len(stages) != 1 || stages[0].Name != "lsp.diagnostics" {
		t.Fatalf("stages = %+v", stages)
	}
	msgs := []agent.Message{
		{Role: agent.RoleUser, Text: "earlier"},
		{Role: agent.RoleUser, Text: "what is broken?"},
	}
	out := stages[0].Run(context.Background(), msgs)
	if len(out) != len(msgs)+1 {
		t.Fatalf("stage produced %d messages, want one more than %d", len(out), len(msgs))
	}
	if out[len(out)-1].Text != "what is broken?" {
		t.Fatal("the user's message must stay last")
	}
	block := out[len(out)-2]
	if block.Role != agent.RoleSystem || !strings.Contains(block.Text, "unused variable x") {
		t.Fatalf("injected block = %+v", block)
	}
	if !strings.Contains(block.Text, "warning") {
		t.Fatalf("severity not rendered: %q", block.Text)
	}
}

// TestStageSaysNothingWhenNothingIsWrong pins that a clean tree costs no
// context. A block asserting "no problems" every turn spends tokens to say
// nothing and trains the model to skip the section.
func TestStageSaysNothingWhenNothingIsWrong(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n"})
	ext := newStubExtension(t, root, stubScript{}, WriteSpec{Tool: "edit_file", Paths: pathArg})

	if _, err := ext.Middleware()[0](context.Background(), callInfo("edit_file", "a.go"), okResult("edited")); err != nil {
		t.Fatalf("middleware: %v", err)
	}
	msgs := []agent.Message{{Role: agent.RoleUser, Text: "hello"}}
	if out := ext.ContextStages()[0].Run(context.Background(), msgs); len(out) != len(msgs) {
		t.Fatalf("stage added %d message(s) for a clean workspace", len(out)-len(msgs))
	}
}

// TestNoServersMeansNoContributions pins that this can be wired
// unconditionally: a surface with no language server configured gets an
// extension that adds no tools, no prompt, no middleware, and no stage.
func TestNoServersMeansNoContributions(t *testing.T) {
	ext, err := New(Config{Roots: []string{workspace(t, nil)}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = ext.Close() })

	src, err := ext.Tools()
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if src != nil {
		t.Fatal("tools registered with no server to answer them")
	}
	if ext.Middleware() != nil || ext.ContextStages() != nil || ext.PromptSections() != nil {
		t.Fatal("an extension with no servers contributed to a seam")
	}
}

func TestNewRequiresARoot(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("want an error for a missing Root")
	}
}

func TestNewRejectsAnIncompleteWriteSpec(t *testing.T) {
	_, err := New(Config{Roots: []string{workspace(t, nil)}, Writes: []WriteSpec{{Tool: "edit_file"}}})
	if err == nil {
		t.Fatal("want an error for a WriteSpec with no Paths")
	}
}

// TestMiddlewareSkipsADeniedCall pins the interaction with the approval ladder.
// The host appends its permission gate last, so the gate is innermost and a
// denial comes back to this middleware as an error. Re-checking then would
// report the file as it stood before a write that never happened.
func TestMiddlewareSkipsADeniedCall(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n"})
	ext := newStubExtension(t, root, stubScript{Diagnostics: map[string][]diagnostic{
		"a.go": {{Message: "undefined: foo"}},
	}}, WriteSpec{Tool: "edit_file", Paths: pathArg})

	denied := func(context.Context, agent.ToolCallInfo) (*core.ToolResult, error) {
		return nil, agent.DenyTool("the user said no")
	}
	res, err := ext.Middleware()[0](context.Background(), callInfo("edit_file", "a.go"), denied)
	if err == nil {
		t.Fatal("a denial must pass through unchanged")
	}
	if res != nil {
		t.Fatalf("a denied call produced a result: %+v", res)
	}
}

// TestMiddlewareDoesNotReportCleanForAProvisionalPublication is #1303 at the
// level the model actually sees it. Before the settle logic this appended
// "no problems reported" to an edit that broke the build, because the server's
// first publication after the change was an empty placeholder.
func TestMiddlewareDoesNotReportCleanForAProvisionalPublication(t *testing.T) {
	root := workspace(t, map[string]string{"a.go": "package a\n"})
	spec := stubSpec(t, stubScript{
		EmptyFirst:     true,
		PublishDelayMs: 300,
		Diagnostics: map[string][]diagnostic{
			"a.go": {{Range: textRange{Start: position{Line: 2}}, Severity: severityError, Message: "undefined: foo"}},
		},
	})
	spec.SettleDelay = 2 * time.Second
	ext, err := New(Config{
		Roots:              []string{root},
		Servers:            []ServerSpec{spec},
		Writes:             []WriteSpec{{Tool: "edit_file", Paths: pathArg}},
		DiagnosticsTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = ext.Close() })

	res, err := ext.Middleware()[0](context.Background(), callInfo("edit_file", "a.go"), okResult("edited"))
	if err != nil {
		t.Fatalf("middleware: %v", err)
	}
	got := resultText(res)
	if strings.Contains(got, "no problems reported") {
		t.Fatalf("a broken edit was reported as clean: %q", got)
	}
	if !strings.Contains(got, "undefined: foo") {
		t.Fatalf("the real diagnostic never reached the model: %q", got)
	}
}
