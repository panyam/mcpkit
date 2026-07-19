package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
)

func childRunner(t *testing.T, turns ...StubTurn) *Runner {
	t.Helper()
	r, err := NewRunner(RunnerConfig{Provider: NewStubProvider(turns...)})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAgentSource_DelegatesAndIsolates(t *testing.T) {
	ctx := context.Background()
	childStub := NewStubProvider(StubTurn{Text: "the child answer"})
	child, _ := NewRunner(RunnerConfig{Provider: childStub})
	as, err := NewAgentSource(AgentSourceConfig{Name: "researcher", Description: "does research", Runner: child})
	if err != nil {
		t.Fatal(err)
	}

	// the tool is exposed under its name
	defs, _ := as.Tools(ctx)
	if len(defs) != 1 || defs[0].Name != "researcher" {
		t.Fatalf("Tools = %+v, want one 'researcher'", defs)
	}

	res, err := as.Call(ctx, "researcher", map[string]any{"task": "find the answer"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || res.Content[0].Text != "the child answer" {
		t.Fatalf("call = %+v, want the child's final text", res)
	}

	// isolation: the child saw ONLY the task, not any parent history
	got := childStub.Requests()[0].Messages
	if len(got) != 1 || got[0].Role != RoleUser || got[0].Text != "find the answer" {
		t.Fatalf("child history = %+v, want just the task (isolated)", got)
	}
}

// TestAgentSource_Supervision drives a coordinator Runner whose tools are a
// MultiSource of two AgentSources — the "supervision falls out for free"
// claim, no new code.
func TestAgentSource_Supervision(t *testing.T) {
	ctx := context.Background()
	calc, _ := NewAgentSource(AgentSourceConfig{
		Name: "calc", Description: "arithmetic", Runner: childRunner(t, StubTurn{Text: "42"})})
	poet, _ := NewAgentSource(AgentSourceConfig{
		Name: "poet", Description: "verse", Runner: childRunner(t, StubTurn{Text: "a haiku"})})

	tools := NewMultiSource()
	if err := tools.Add("calc-agent", calc); err != nil {
		t.Fatal(err)
	}
	if err := tools.Add("poet-agent", poet); err != nil {
		t.Fatal(err)
	}

	coordStub := NewStubProvider(
		StubTurn{ToolCalls: []ToolCall{{ID: "c1", Name: "calc", Args: core.NewRawJSON(json.RawMessage(`{"task":"2+2"}`))}}},
		StubTurn{Text: "the sub-agent said 42"},
	)
	coord, _ := NewRunner(RunnerConfig{Provider: coordStub, Tools: tools})

	result, err := coord.Run(ctx, []Message{{Role: RoleUser, Text: "add two and two"}}, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "the sub-agent said 42" {
		t.Fatalf("coordinator final = %q", result.Text)
	}
	// the child's answer came back as the calc tool result
	fed := coordStub.Requests()[1].Messages
	var sawChild bool
	for _, m := range fed {
		if m.Role == RoleTool && strings.Contains(m.Text, "42") {
			sawChild = true
		}
	}
	if !sawChild {
		t.Fatal("child answer did not return to the coordinator as a tool result")
	}
}

func TestAgentSource_DepthCap(t *testing.T) {
	as, _ := NewAgentSource(AgentSourceConfig{
		Name: "deep", Description: "d", Runner: childRunner(t, StubTurn{Text: "x"}), MaxDepth: 2})

	// at depth 2 (== MaxDepth) the call is refused before running
	res, err := as.Call(withAgentDepth(context.Background(), 2), "deep", map[string]any{"task": "go"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content[0].Text, "max depth") {
		t.Fatalf("depth cap = %+v, want an IsError max-depth refusal", res)
	}
}

func TestAgentSource_CallBudget(t *testing.T) {
	as, _ := NewAgentSource(AgentSourceConfig{
		Name: "b", Description: "d", Runner: childRunner(t, StubTurn{Text: "1"}, StubTurn{Text: "2"})})
	ctx := WithAgentCallBudget(context.Background(), 1)

	if res, _ := as.Call(ctx, "b", map[string]any{"task": "go"}); res.IsError {
		t.Fatalf("first call within budget should succeed: %+v", res)
	}
	res, _ := as.Call(ctx, "b", map[string]any{"task": "go"})
	if !res.IsError || !strings.Contains(res.Content[0].Text, "budget") {
		t.Fatalf("second call over budget = %+v, want an IsError budget refusal", res)
	}
}

func TestAgentSource_MissingTaskAndValidation(t *testing.T) {
	as, _ := NewAgentSource(AgentSourceConfig{Name: "x", Description: "d", Runner: childRunner(t, StubTurn{Text: "ok"})})
	if res, _ := as.Call(context.Background(), "x", map[string]any{}); !res.IsError {
		t.Fatal("missing task should be an IsError result")
	}
	// unknown name is a dispatch error, not a result
	if _, err := as.Call(context.Background(), "nope", map[string]any{"task": "y"}); err == nil {
		t.Fatal("unknown tool name should be a dispatch error")
	}

	if _, err := NewAgentSource(AgentSourceConfig{Name: "x"}); err == nil {
		t.Fatal("missing Runner should error")
	}
	if _, err := NewAgentSource(AgentSourceConfig{Runner: childRunner(t, StubTurn{Text: "z"})}); err == nil {
		t.Fatal("missing Name should error")
	}
}
