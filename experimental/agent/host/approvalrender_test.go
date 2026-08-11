package host

import (
	"context"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/experimental/agent"
	"github.com/panyam/mcpkit/core"
)

func callInfo(name, args string) agent.ToolCallInfo {
	return agent.ToolCallInfo{
		Call: agent.ToolCall{Name: name, Args: core.NewRawJSON([]byte(args))},
	}
}

// rendererExt is a minimal extension that contributes nothing but renderers,
// which is also the shape a caller uses when they want a renderer without a
// tool of their own.
type rendererExt struct {
	BaseExtension
	name      string
	renderers []ApprovalRenderer
}

func (e rendererExt) Name() string                          { return e.name }
func (e rendererExt) ApprovalRenderers() []ApprovalRenderer { return e.renderers }

// appWith runs the real applyExtensions rather than assigning renderers
// directly, so these tests cover the collection wiring too and not just
// renderApproval in isolation.
func appWith(t *testing.T, exts ...Extension) *App {
	t.Helper()
	a := &App{
		promptBuilder: &SystemPromptBuilder{},
		commands:      NewCommandRegistry(),
	}
	if _, err := a.applyExtensions(exts); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestRendererTextReachesTheAsk(t *testing.T) {
	a := appWith(t, rendererExt{name: "x", renderers: []ApprovalRenderer{
		func(context.Context, agent.ToolCallInfo) (string, bool) { return "Apply 2 changes to main.go?", true },
	}})

	got := a.renderApproval(context.Background(), callInfo("edit_file", `{"path":"main.go"}`))
	if got != "Apply 2 changes to main.go?" {
		t.Errorf("renderApproval() = %q, want the renderer's text", got)
	}
}

// TestDecliningFallsBackToTheDefault is what lets a renderer claim a subset
// without reproducing the generic format for everything else.
func TestDecliningFallsBackToTheDefault(t *testing.T) {
	a := appWith(t, rendererExt{name: "x", renderers: []ApprovalRenderer{
		func(context.Context, agent.ToolCallInfo) (string, bool) { return "", false },
	}})

	got := a.renderApproval(context.Background(), callInfo("other_tool", `{"a":1}`))
	if !strings.Contains(got, "Allow tool call") {
		t.Errorf("a declined call should get the built-in format, got %q", got)
	}
}

// TestEmptyTextCountsAsDeclining guards a renderer that claims a call and then
// produces nothing. Passing that through would ask the user to confirm a blank
// question, which they can only answer by guessing.
func TestEmptyTextCountsAsDeclining(t *testing.T) {
	a := appWith(t, rendererExt{name: "x", renderers: []ApprovalRenderer{
		func(context.Context, agent.ToolCallInfo) (string, bool) { return "", true },
	}})

	got := a.renderApproval(context.Background(), callInfo("edit_file", `{"path":"main.go"}`))
	if !strings.Contains(got, "Allow tool call") {
		t.Errorf("an empty render should fall back, got %q", got)
	}
}

func TestFirstClaimWins(t *testing.T) {
	a := appWith(t,
		rendererExt{name: "first", renderers: []ApprovalRenderer{
			func(context.Context, agent.ToolCallInfo) (string, bool) { return "first", true },
		}},
		rendererExt{name: "second", renderers: []ApprovalRenderer{
			func(context.Context, agent.ToolCallInfo) (string, bool) { return "second", true },
		}},
	)

	if got := a.renderApproval(context.Background(), callInfo("edit_file", `{}`)); got != "first" {
		t.Errorf("renderApproval() = %q, want the first registered renderer", got)
	}
}

// TestRendererSeesPostMiddlewareArguments is the security property this seam
// has to preserve. A user approves what they were shown, so what they are
// shown must be the call that runs, not the one the model proposed. If a
// middleware rewrote the arguments and the prompt rendered the original, the
// approval would be for a call that never executes.
func TestRendererSeesPostMiddlewareArguments(t *testing.T) {
	var seen string
	a := appWith(t, rendererExt{name: "x", renderers: []ApprovalRenderer{
		func(_ context.Context, info agent.ToolCallInfo) (string, bool) {
			seen = string(info.Call.Args.Raw())
			return "rendered", true
		},
	}})

	// ToolCallInfo.Call.Args is documented as the arguments "as rewritten by
	// any earlier middleware rather than as the model produced them", so the
	// rewritten value is what a renderer must receive.
	rewritten := `{"path":"REWRITTEN.md"}`
	a.renderApproval(context.Background(), callInfo("edit_file", rewritten))

	if seen != rewritten {
		t.Fatalf("renderer saw %q, want the post-middleware arguments %q", seen, rewritten)
	}
}

func TestNoRenderersLeavesTheDefaultUntouched(t *testing.T) {
	a := appWith(t)
	info := callInfo("edit_file", `{"path":"main.go"}`)

	if got, want := a.renderApproval(context.Background(), info), approvalPrompt(info); got != want {
		t.Errorf("with no renderers the prompt must be unchanged:\n got %q\nwant %q", got, want)
	}
}

// TestDefaultStillTrimsLargeArguments keeps the fallback's own behaviour. The
// trim is right for an unknown tool whose arguments are an opaque blob; what
// the seam changes is that it is no longer imposed on tools that know better.
func TestDefaultStillTrimsLargeArguments(t *testing.T) {
	a := appWith(t)
	big := `{"blob":"` + strings.Repeat("x", 500) + `"}`

	got := a.renderApproval(context.Background(), callInfo("unknown_tool", big))
	if !strings.Contains(got, "…") {
		t.Errorf("the default should still trim a large payload:\n%s", got)
	}
	if len(got) > 300 {
		t.Errorf("trimmed prompt is %d chars, want it bounded", len(got))
	}
}

// TestCustomRendererIsNotTrimmed is the other half: a renderer that decided a
// long prompt is correct gets it through intact. A diff trimmed to 200
// characters is unreviewable, which is the whole reason for the seam.
func TestCustomRendererIsNotTrimmed(t *testing.T) {
	long := "Apply 1 change:\n" + strings.Repeat("  - a line of context\n", 60)
	a := appWith(t, rendererExt{name: "x", renderers: []ApprovalRenderer{
		func(context.Context, agent.ToolCallInfo) (string, bool) { return long, true },
	}})

	if got := a.renderApproval(context.Background(), callInfo("edit_file", `{}`)); got != long {
		t.Errorf("a renderer's text must not be trimmed: got %d chars, want %d", len(got), len(long))
	}
}

func TestNilRendererIsSkipped(t *testing.T) {
	a := appWith(t, rendererExt{name: "x", renderers: []ApprovalRenderer{
		nil,
		func(context.Context, agent.ToolCallInfo) (string, bool) { return "second", true },
	}})

	if got := a.renderApproval(context.Background(), callInfo("edit_file", `{}`)); got != "second" {
		t.Errorf("a nil renderer should be skipped, got %q", got)
	}
}

func TestRendererReceivesTheContext(t *testing.T) {
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "carried")

	var seen any
	a := appWith(t, rendererExt{name: "x", renderers: []ApprovalRenderer{
		func(c context.Context, _ agent.ToolCallInfo) (string, bool) {
			seen = c.Value(key{})
			return "ok", true
		},
	}})

	a.renderApproval(ctx, callInfo("edit_file", `{}`))
	if seen != "carried" {
		t.Errorf("renderer got %v, want the caller's context", seen)
	}
}
