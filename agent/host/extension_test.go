package host

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/core"
)

// fullExt contributes to every seam at once, which is the point of the
// contract: a feature is one thing with five facets, not five registrations.
type fullExt struct {
	BaseExtension
	name    string
	sawTool bool
}

func (e *fullExt) Name() string { return e.name }

func (e *fullExt) Tools() (agent.ToolSource, error) {
	src := agent.NewFuncSource()
	err := agent.AddFunc(src, "ext_ping", "pings", func(context.Context, struct{}) (string, error) {
		e.sawTool = true
		return "pong", nil
	})
	return src, err
}

func (e *fullExt) Middleware() []agent.ToolMiddleware {
	return []agent.ToolMiddleware{agent.AfterToolCall(
		func(_ context.Context, _ agent.ToolCallInfo, res *core.ToolResult) (*core.ToolResult, error) {
			return &core.ToolResult{Content: []core.Content{{Type: "text", Text: "[wrapped]"}}}, nil
		})}
}

func (e *fullExt) PromptSections() []PromptSection {
	return []PromptSection{PromptSectionFunc(func(context.Context) string { return "EXT PROMPT" })}
}

func (e *fullExt) Commands() []*Command {
	return []*Command{{Name: "extcmd", Help: "an extension command",
		Run: func(context.Context, string) (CmdResult, error) {
			return CmdResult{Kind: CmdMessage, Message: "ran"}, nil
		}}}
}

func (e *fullExt) ContextStages() []ContextStage {
	return []ContextStage{{Name: "ext.note", Run: func(_ context.Context, msgs []agent.Message) []agent.Message {
		return weaveBeforeUser(msgs, []string{"EXT CONTEXT"})
	}}}
}

// minimalExt contributes nothing but a name, proving BaseExtension makes every
// other seam genuinely optional.
type minimalExt struct{ BaseExtension }

func (minimalExt) Name() string { return "minimal" }

func extApp(t *testing.T, stub *agent.StubProvider, exts ...Extension) *App {
	t.Helper()
	ts := startTestServer(t)
	app, err := NewApp(testConfig(ts.URL), &strings.Builder{}, strings.NewReader(""),
		WithProvider(stub), WithExtension(exts...))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.Close() })
	return app
}

// TestExtensionContributesEverySeam is the acceptance: one registration wires
// tools, middleware, a prompt section, a command, and a context stage.
func TestExtensionContributesEverySeam(t *testing.T) {
	ext := &fullExt{name: "full"}
	stub := agent.NewStubProvider(
		agent.StubTurn{ToolCalls: []agent.ToolCall{{
			ID: "c1", Name: "ext_ping", Args: core.NewRawJSON(json.RawMessage(`{}`)),
		}}},
		agent.StubTurn{Text: "done"},
	)
	app := extApp(t, stub, ext)

	// Tools
	defs, err := app.sources.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, d := range defs {
		if strings.Contains(d.Name, "ext_ping") {
			found = true
		}
	}
	if !found {
		t.Fatalf("extension tool not registered: %v", defs)
	}

	// Command
	if _, ok := app.commands.Lookup("extcmd"); !ok {
		t.Fatal("extension command not registered")
	}

	// Prompt section
	if got := app.promptBuilder.Build(context.Background()); !strings.Contains(got, "EXT PROMPT") {
		t.Fatalf("extension prompt section missing:\n%s", got)
	}

	// Context stage, declared last so it sits closest to the user message.
	names := app.context.stageNames()
	if names[len(names)-1] != "ext.note" {
		t.Fatalf("stage order = %v, want ext.note last", names)
	}

	// Middleware: the tool ran, and its result reached the model rewritten.
	if err := app.RunTurn(context.Background(), "ping"); err != nil {
		t.Fatal(err)
	}
	if !ext.sawTool {
		t.Fatal("extension tool was never dispatched")
	}
	// Find by role: the context stage adds a message, so a fixed index would
	// couple this assertion to how many producers happen to be configured.
	var toolText string
	for _, m := range stub.Requests()[1].Messages {
		if m.Role == agent.RoleTool {
			toolText = m.Text
		}
	}
	if toolText != "[wrapped]" {
		t.Fatalf("extension middleware did not wrap the result: %q", toolText)
	}
}

// TestExtensionContextStageReachesTheModel pins that a context stage's block
// lands in the per-turn view, before the user's message.
func TestExtensionContextStageReachesTheModel(t *testing.T) {
	stub := agent.NewStubProvider(agent.StubTurn{Text: "ok"})
	app := extApp(t, stub, &fullExt{name: "full"})

	if err := app.RunTurn(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	msgs := stub.Requests()[0].Messages
	if len(msgs) < 2 {
		t.Fatalf("expected the injected block plus the user message, got %d", len(msgs))
	}
	if msgs[len(msgs)-2].Text != "EXT CONTEXT" || msgs[len(msgs)-1].Text != "hello" {
		t.Fatalf("context stage did not weave before the user message: %+v", msgs)
	}
}

// TestExtensionContextStageIsTransient pins that a stage's block is per-turn:
// writing it into history would re-inject it every turn afterwards.
func TestExtensionContextStageIsTransient(t *testing.T) {
	stub := agent.NewStubProvider(agent.StubTurn{Text: "one"}, agent.StubTurn{Text: "two"})
	app := extApp(t, stub, &fullExt{name: "full"})

	if err := app.RunTurn(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	for _, m := range app.history {
		if m.Text == "EXT CONTEXT" {
			t.Fatal("a transient context stage was written into history")
		}
	}
}

// TestBaseExtensionMakesSeamsOptional pins that an extension contributing only
// a name constructs cleanly and adds nothing.
func TestBaseExtensionMakesSeamsOptional(t *testing.T) {
	app := extApp(t, agent.NewStubProvider(agent.StubTurn{Text: "ok"}), minimalExt{})
	if got := app.context.stageNames(); got[len(got)-1] == "ext.note" {
		t.Fatal("minimal extension contributed a stage")
	}
	if err := app.RunTurn(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
}

// TestExtensionDuplicateNameFails pins that two extensions claiming one name
// fail construction rather than one silently winning the tool namespace.
func TestExtensionDuplicateNameFails(t *testing.T) {
	ts := startTestServer(t)
	_, err := NewApp(testConfig(ts.URL), &strings.Builder{}, strings.NewReader(""),
		WithProvider(agent.NewStubProvider(agent.StubTurn{Text: "ok"})),
		WithExtension(&fullExt{name: "dup"}, &fullExt{name: "dup"}))
	if err == nil {
		t.Fatal("duplicate extension names must fail construction")
	}
	if !strings.Contains(err.Error(), "duplicate extension") {
		t.Fatalf("unhelpful error: %v", err)
	}
}

// TestExtensionCommandCollisionFails pins that an extension cannot shadow a
// built-in command. Register overwrites silently, so this has to be refused
// before it reaches the registry — a shadowed /command fails at use, far from
// the cause.
func TestExtensionCommandCollisionFails(t *testing.T) {
	ts := startTestServer(t)
	_, err := NewApp(testConfig(ts.URL), &strings.Builder{}, strings.NewReader(""),
		WithProvider(agent.NewStubProvider(agent.StubTurn{Text: "ok"})),
		WithExtension(&collidingExt{}))
	if err == nil {
		t.Fatal("an extension shadowing a built-in command must fail construction")
	}
	if !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("unhelpful error: %v", err)
	}
}

type collidingExt struct{ BaseExtension }

func (collidingExt) Name() string { return "collider" }
func (collidingExt) Commands() []*Command {
	return []*Command{{Name: "tools", Help: "shadows the built-in"}}
}

// turnExt counts TurnStart calls and can fail on demand.
type turnExt struct {
	BaseExtension
	starts int
	fail   error
}

func (turnExt) Name() string { return "turns" }

func (e *turnExt) TurnStart(context.Context) error {
	e.starts++
	return e.fail
}

// TestTurnStartRunsOncePerTurn is the wiring acceptance for the lifecycle
// seam. Extension had no per-turn hook before this, so an extension whose
// state is scoped to a turn had to abuse a ContextStage for its side effect.
func TestTurnStartRunsOncePerTurn(t *testing.T) {
	ext := &turnExt{}
	stub := agent.NewStubProvider(agent.StubTurn{Text: "one"}, agent.StubTurn{Text: "two"})
	app := extApp(t, stub, ext)

	for i := 1; i <= 2; i++ {
		if err := app.RunTurn(context.Background(), "hi"); err != nil {
			t.Fatal(err)
		}
		if ext.starts != i {
			t.Fatalf("after %d turns, TurnStart ran %d times", i, ext.starts)
		}
	}
}

// TestTurnStartErrorAbortsBeforeHistory pins that a failed start leaves
// nothing half-done: the user's message must not be in history if the turn
// never ran.
func TestTurnStartErrorAbortsBeforeHistory(t *testing.T) {
	ext := &turnExt{fail: errors.New("no space left")}
	stub := agent.NewStubProvider(agent.StubTurn{Text: "unreachable"})
	app := extApp(t, stub, ext)

	before := len(app.history)
	err := app.RunTurn(context.Background(), "hi")
	if err == nil {
		t.Fatal("a TurnStart failure should abort the turn")
	}
	if !strings.Contains(err.Error(), "turns") {
		t.Fatalf("error should name the extension, got %v", err)
	}
	if len(app.history) != before {
		t.Fatalf("history grew by %d despite the abort", len(app.history)-before)
	}
}
