package exec

import (
	"context"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/agent"
	"github.com/panyam/mcpkit/experimental/agent/host"
)

func mustExtension(t *testing.T, cfg Config) *Extension {
	t.Helper()
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestExtensionRegistersItsCommandsAsTools(t *testing.T) {
	e := mustExtension(t, baseConfig(t, echoSpec()))
	if e.Name() != "exec" {
		t.Errorf("Name() = %q", e.Name())
	}
	src, err := e.Tools()
	if err != nil {
		t.Fatal(err)
	}
	defs, err := src.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 || defs[0].Name != "run_echo" {
		t.Fatalf("want one run_echo tool, got %+v", defs)
	}
}

// TestApprovalShowsTheCommandRatherThanItsArguments is the reason this
// extension supplies a renderer. The default renders the call's JSON, which
// for these tools is an argument array with no sign of the command it attaches
// to, so the user approves a fragment.
func TestApprovalShowsTheCommandRatherThanItsArguments(t *testing.T) {
	spec := CommandSpec{
		Name:        "test",
		Argv:        []string{"echo", "test"},
		Description: "Run the tests.",
		Args:        &ArgPolicy{Max: 2, Match: `[\w./-]+`},
	}
	e := mustExtension(t, baseConfig(t, spec))

	got, ok := e.renderApproval(context.Background(), agent.ToolCallInfo{
		Call: agent.ToolCall{Name: "run_test", Args: core.NewRawJSON([]byte(`{"args":["./pkg"]}`))},
	})
	if !ok {
		t.Fatalf("the extension must claim its own tools, got %q", got)
	}
	for _, want := range []string{"echo test ./pkg", "confined by unconfined"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt is missing %q:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "no network") {
		t.Errorf("whether the command can reach the network is part of what is being approved:\n%s", got)
	}
}

func TestApprovalDeclinesToolsThisExtensionDoesNotOwn(t *testing.T) {
	e := mustExtension(t, baseConfig(t, echoSpec()))
	if _, ok := e.renderApproval(context.Background(), agent.ToolCallInfo{
		Call: agent.ToolCall{Name: "edit_file", Args: core.NewRawJSON([]byte(`{}`))},
	}); ok {
		t.Error("claiming another package's tool would replace a renderer that knows how to show it")
	}
}

func TestApprovalSaysWhenTheCommandCanReachTheNetwork(t *testing.T) {
	spec := echoSpec()
	spec.AllowNetwork = true
	e := mustExtension(t, baseConfig(t, spec))
	got, _ := e.renderApproval(context.Background(), agent.ToolCallInfo{
		Call: agent.ToolCall{Name: "run_echo", Args: core.NewRawJSON(nil)},
	})
	if !strings.Contains(got, "network allowed") {
		t.Errorf("a command with the network open must say so:\n%s", got)
	}
}

func TestPromptTellsTheModelHowToReadAFailure(t *testing.T) {
	e := mustExtension(t, baseConfig(t, echoSpec()))
	sections := e.PromptSections()
	if len(sections) != 1 {
		t.Fatalf("want one section, got %d", len(sections))
	}
	got := sections[0].Section(context.Background())
	for _, want := range []string{"non-zero exit", "capped", "fixed"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt does not cover %q:\n%s", want, got)
		}
	}
}

var _ host.Extension = (*Extension)(nil)
