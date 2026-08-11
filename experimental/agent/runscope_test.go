package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/panyam/mcpkit/core"
)

func TestAgentPathBasics(t *testing.T) {
	var p AgentPath
	if p.Depth() != 0 || p.String() != "" {
		t.Fatalf("zero path = depth %d, %q", p.Depth(), p.String())
	}

	child := p.Child("researcher").Child("summarizer")
	if child.Depth() != 2 {
		t.Fatalf("depth = %d, want 2", child.Depth())
	}
	if got := child.String(); got != "researcher/summarizer" {
		t.Fatalf("String() = %q", got)
	}
	if !child.Contains("researcher") || !child.Contains("summarizer") {
		t.Fatalf("Contains missed an ancestor: %v", child)
	}
	if p.Depth() != 0 {
		t.Fatal("Child mutated the receiver")
	}
}

// TestAgentPathContainsIsExact is why hops are a list rather than a joined
// string: substring matching answers yes for a name that merely shares a
// prefix, which would silently mis-scope a middleware.
func TestAgentPathContainsIsExact(t *testing.T) {
	p := AgentPath{}.Child("researcher")
	if p.Contains("research") {
		t.Fatal(`Contains("research") matched the ancestor "researcher"`)
	}
	if strings.Contains(p.String(), "research") == false {
		t.Fatal("precondition: the joined form does contain the prefix, which is the trap")
	}
}

// TestAgentPathToleratesSeparatorInName pins the other half: a name carrying a
// slash cannot forge an extra level, which splitting a joined path would.
func TestAgentPathToleratesSeparatorInName(t *testing.T) {
	p := AgentPath{}.Child("a/b").Child("c")
	if p.Depth() != 2 {
		t.Fatalf("depth = %d, want 2 — a slash in a name must not add a level", p.Depth())
	}
	if !p.Contains("a/b") {
		t.Fatal(`Contains("a/b") failed`)
	}
	if p.Contains("a") || p.Contains("b") {
		t.Fatal("a slash in a name leaked as separate hops")
	}
}

// TestRunScopeRoundTrip is the A2 obligation: a scope may cross a parent/child
// boundary, so it has to survive encoding/json unchanged.
func TestRunScopeRoundTrip(t *testing.T) {
	want := RunScope{
		Path:       AgentPath{}.Child("researcher").Child("summarizer"),
		CallBudget: 3,
		Tree:       TreeUsage{StepsUsed: 2, MaxSteps: 10, TokensUsed: 500, MaxTokens: 2000},
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got RunScope
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round-trip drift:\n got %+v\nwant %+v", got, want)
	}
}

// TestScopeFromBareContext pins the outside-a-run reading: top level, nothing
// bounded. CallBudget must be -1 rather than 0, which would read as exhausted.
func TestScopeFromBareContext(t *testing.T) {
	s := ScopeFrom(context.Background())
	if s.Depth() != 0 || len(s.Path) != 0 {
		t.Fatalf("bare ctx scope = %+v, want empty path", s)
	}
	if s.CallBudget != -1 {
		t.Fatalf("CallBudget = %d, want -1 (unbounded)", s.CallBudget)
	}
}

// scopeProbe records the scope every tool call was dispatched with, keyed by
// tool name, so a test can compare a parent's view with its child's.
type scopeProbe struct {
	mu   sync.Mutex
	seen map[string]RunScope
}

func newScopeProbe() *scopeProbe { return &scopeProbe{seen: map[string]RunScope{}} }

func (p *scopeProbe) middleware() ToolMiddleware {
	return func(ctx context.Context, info ToolCallInfo, next ToolCallFunc) (*core.ToolResult, error) {
		p.mu.Lock()
		p.seen[info.Call.Name] = info.Scope
		p.mu.Unlock()
		return next(ctx, info)
	}
}

func (p *scopeProbe) get(tool string) RunScope {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.seen[tool]
}

// TestScopeDepthInsideSubAgent is the ticket's named acceptance: a middleware
// running inside a sub-agent reads a non-empty path while the parent's reads
// empty. Without it a checkpoint extension snapshots on every nested call.
func TestScopeDepthInsideSubAgent(t *testing.T) {
	probe := newScopeProbe()

	childTools := NewFuncSource()
	if err := AddFunc(childTools, "child_tool", "runs in the child", func(context.Context, struct{}) (string, error) {
		return "ok", nil
	}); err != nil {
		t.Fatal(err)
	}
	child, err := NewRunner(RunnerConfig{
		Provider: NewStubProvider(
			StubTurn{ToolCalls: []ToolCall{{ID: "k1", Name: "child_tool", Args: core.NewRawJSON([]byte(`{}`))}}},
			StubTurn{Text: "child done"},
		),
		Tools:          childTools,
		ToolMiddleware: []ToolMiddleware{probe.middleware()},
	})
	if err != nil {
		t.Fatal(err)
	}

	sub, err := NewAgentSource(AgentSourceConfig{
		Name: "researcher", Description: "researches", Runner: child,
	})
	if err != nil {
		t.Fatal(err)
	}
	parentTools := NewMultiSource()
	if err := parentTools.Add("sub", sub); err != nil {
		t.Fatal(err)
	}
	own := NewFuncSource()
	if err := AddFunc(own, "parent_tool", "runs at the top", func(context.Context, struct{}) (string, error) {
		return "ok", nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := parentTools.Add("own", own); err != nil {
		t.Fatal(err)
	}

	parent, err := NewRunner(RunnerConfig{
		Provider: NewStubProvider(
			StubTurn{ToolCalls: []ToolCall{{ID: "p1", Name: "parent_tool", Args: core.NewRawJSON([]byte(`{}`))}}},
			StubTurn{ToolCalls: []ToolCall{{ID: "p2", Name: "researcher", Args: core.NewRawJSON([]byte(`{"task":"go"}`))}}},
			StubTurn{Text: "parent done"},
		),
		Tools:          parentTools,
		ToolMiddleware: []ToolMiddleware{probe.middleware()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parent.Run(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}

	top := probe.get("parent_tool")
	if top.Depth() != 0 {
		t.Fatalf("top-level call saw depth %d, want 0 (path %v)", top.Depth(), top.Path)
	}
	nested := probe.get("child_tool")
	if nested.Depth() != 1 {
		t.Fatalf("sub-agent call saw depth %d, want 1 (path %v)", nested.Depth(), nested.Path)
	}
	if !nested.Path.Contains("researcher") {
		t.Fatalf("sub-agent path = %v, want it to contain the persona name", nested.Path)
	}
}

// TestScopeCallBudgetVisible pins that an extension can read the remaining
// sub-agent budget, which is what lets it back off before exhaustion instead
// of discovering ErrTreeBudget.
func TestScopeCallBudgetVisible(t *testing.T) {
	probe := newScopeProbe()
	src := NewFuncSource()
	if err := AddFunc(src, "t", "a tool", func(context.Context, struct{}) (string, error) { return "ok", nil }); err != nil {
		t.Fatal(err)
	}
	r, err := NewRunner(RunnerConfig{
		Provider: NewStubProvider(
			StubTurn{ToolCalls: []ToolCall{{ID: "c1", Name: "t", Args: core.NewRawJSON([]byte(`{}`))}}},
			StubTurn{Text: "done"},
		),
		Tools:          src,
		ToolMiddleware: []ToolMiddleware{probe.middleware()},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithAgentCallBudget(context.Background(), 5)
	if _, err := r.Run(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := probe.get("t").CallBudget; got != 5 {
		t.Fatalf("CallBudget = %d, want 5", got)
	}
}

// TestScopeTreeUsageVisible pins the other half of budget awareness: caps and
// consumption both reach the middleware.
func TestScopeTreeUsageVisible(t *testing.T) {
	probe := newScopeProbe()
	src := NewFuncSource()
	if err := AddFunc(src, "t", "a tool", func(context.Context, struct{}) (string, error) { return "ok", nil }); err != nil {
		t.Fatal(err)
	}
	r, err := NewRunner(RunnerConfig{
		Provider: NewStubProvider(
			StubTurn{ToolCalls: []ToolCall{{ID: "c1", Name: "t", Args: core.NewRawJSON([]byte(`{}`))}}},
			StubTurn{Text: "done"},
		),
		Tools:          src,
		ToolMiddleware: []ToolMiddleware{probe.middleware()},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithTreeBudget(context.Background(), TreeBudget{MaxSteps: 10, MaxTokens: 9999})
	if _, err := r.Run(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	got := probe.get("t").Tree
	if got.MaxSteps != 10 || got.MaxTokens != 9999 {
		t.Fatalf("caps not surfaced: %+v", got)
	}
	if got.StepsUsed <= 0 {
		t.Fatalf("StepsUsed = %d, want the step already consumed to be visible", got.StepsUsed)
	}
}

// TestScopeUnbudgetedReadsUnbounded pins that a turn with no TreeBudget
// reports zero caps rather than a false limit an extension might respect.
func TestScopeUnbudgetedReadsUnbounded(t *testing.T) {
	probe := newScopeProbe()
	src := NewFuncSource()
	if err := AddFunc(src, "t", "a tool", func(context.Context, struct{}) (string, error) { return "ok", nil }); err != nil {
		t.Fatal(err)
	}
	r, err := NewRunner(RunnerConfig{
		Provider: NewStubProvider(
			StubTurn{ToolCalls: []ToolCall{{ID: "c1", Name: "t", Args: core.NewRawJSON([]byte(`{}`))}}},
			StubTurn{Text: "done"},
		),
		Tools:          src,
		ToolMiddleware: []ToolMiddleware{probe.middleware()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	s := probe.get("t")
	if s.Tree.MaxSteps != 0 || s.Tree.MaxTokens != 0 {
		t.Fatalf("unbudgeted turn reported caps: %+v", s.Tree)
	}
	if s.CallBudget != -1 {
		t.Fatalf("CallBudget = %d, want -1", s.CallBudget)
	}
}

// TestScopeFanOutMembersAreIndependent is the aliasing check that AgentPath.Child
// exists for. Fan-out members run concurrently off one parent context; if Child
// appended into a shared backing array, two members would see each other's hop
// or race on the same slice. Run under -race.
func TestScopeFanOutMembersAreIndependent(t *testing.T) {
	probe := newScopeProbe()

	member := func(name, tool string) *AgentSource {
		t.Helper()
		src := NewFuncSource()
		if err := AddFunc(src, tool, "member tool", func(context.Context, struct{}) (string, error) {
			return "ok", nil
		}); err != nil {
			t.Fatal(err)
		}
		r, err := NewRunner(RunnerConfig{
			Provider: NewStubProvider(
				StubTurn{ToolCalls: []ToolCall{{ID: "m", Name: tool, Args: core.NewRawJSON([]byte(`{}`))}}},
				StubTurn{Text: "member done"},
			),
			Tools:          src,
			ToolMiddleware: []ToolMiddleware{probe.middleware()},
		})
		if err != nil {
			t.Fatal(err)
		}
		as, err := NewAgentSource(AgentSourceConfig{Name: name, Description: name, Runner: r})
		if err != nil {
			t.Fatal(err)
		}
		return as
	}

	fan, err := NewFanOutSource(FanOutConfig{
		Name: "ensemble", Description: "broadcasts",
		Members: []*AgentSource{member("alpha", "alpha_tool"), member("beta", "beta_tool")},
	})
	if err != nil {
		t.Fatal(err)
	}

	parent, err := NewRunner(RunnerConfig{
		Provider: NewStubProvider(
			StubTurn{ToolCalls: []ToolCall{{ID: "f1", Name: "ensemble", Args: core.NewRawJSON([]byte(`{"task":"go"}`))}}},
			StubTurn{Text: "parent done"},
		),
		Tools: fan,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parent.Run(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}

	a, b := probe.get("alpha_tool"), probe.get("beta_tool")
	if a.Depth() != 1 || b.Depth() != 1 {
		t.Fatalf("member depths = %d and %d, want 1 each", a.Depth(), b.Depth())
	}
	if !a.Path.Contains("alpha") || a.Path.Contains("beta") {
		t.Fatalf("alpha saw path %v — a member must not see a sibling's hop", a.Path)
	}
	if !b.Path.Contains("beta") || b.Path.Contains("alpha") {
		t.Fatalf("beta saw path %v — a member must not see a sibling's hop", b.Path)
	}
}
