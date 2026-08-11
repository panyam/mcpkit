package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/panyam/mcpkit/core"
)

// spinTurn is a stub turn that always calls a no-op tool, so a Runner keeps
// looping (one step per turn) until a step cap stops it.
func spinTurn() StubTurn {
	return StubTurn{ToolCalls: []ToolCall{{ID: "s", Name: "noop", Args: core.NewRawJSON(json.RawMessage(`{}`))}}}
}

func noopSource(t *testing.T) *FuncSource {
	t.Helper()
	src := NewFuncSource()
	AddFunc(src, "noop", "does nothing", func(ctx context.Context, in struct{}) (string, error) { return "ok", nil })
	return src
}

// TestTreeBudget_StepCapAbortsTurn asserts the aggregate step budget stops a
// chatty single Runner before its own MaxSteps, with ErrTreeBudget.
func TestTreeBudget_StepCapAbortsTurn(t *testing.T) {
	// The provider would spin forever; MaxSteps is high, but the tree budget of
	// 3 steps must stop it first.
	turns := make([]StubTurn, 10)
	for i := range turns {
		turns[i] = spinTurn()
	}
	stub := NewStubProvider(turns...)
	r, err := NewRunner(RunnerConfig{Provider: stub, Tools: noopSource(t), MaxSteps: 20, TreeBudget: TreeBudget{MaxSteps: 3}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Run(context.Background(), []Message{{Role: RoleUser, Text: "go"}}, nil)
	if !errors.Is(err, ErrTreeBudget) {
		t.Fatalf("want ErrTreeBudget, got %v", err)
	}
	// exactly 3 model calls happened (the budget), not 20 (MaxSteps).
	if n := len(stub.Requests()); n != 3 {
		t.Fatalf("tree step budget = 3, but %d model calls happened", n)
	}
}

// usageTurn is a text turn that also reports token usage.
func usageTurn(text string, in, out int) StubTurn {
	return StubTurn{Deltas: []Delta{
		{Kind: DeltaText, Text: text},
		{Kind: DeltaUsage, Usage: &Usage{InputTokens: in, OutputTokens: out}},
		{Kind: DeltaFinish, FinishReason: "tool_calls"},
	}}
}

// spinUsageTurn spins (calls noop) AND reports usage, so a multi-step run
// accumulates tokens across steps.
func spinUsageTurn(in, out int) StubTurn {
	return StubTurn{Deltas: []Delta{
		{Kind: DeltaToolCallStart, Index: 0, ToolCallID: "s", ToolName: "noop", Text: "{}"},
		{Kind: DeltaUsage, Usage: &Usage{InputTokens: in, OutputTokens: out}},
		{Kind: DeltaFinish, FinishReason: "tool_calls"},
	}}
}

// TestTreeBudget_TokenCapAbortsTurn asserts the token budget stops the turn once
// cumulative usage exceeds MaxTokens (post-hoc: after the step that crosses it).
func TestTreeBudget_TokenCapAbortsTurn(t *testing.T) {
	// each step reports 40 tokens; a 90-token budget allows steps 1 and 2 (80),
	// then step 3's top-of-loop check sees 80 < 90 so it runs (120), and step 4
	// is rejected. So 3 model calls, then ErrTreeBudget.
	turns := make([]StubTurn, 10)
	for i := range turns {
		turns[i] = spinUsageTurn(30, 10)
	}
	stub := NewStubProvider(turns...)
	r, _ := NewRunner(RunnerConfig{Provider: stub, Tools: noopSource(t), MaxSteps: 20, TreeBudget: TreeBudget{MaxTokens: 90}})
	_, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "go"}}, nil)
	if !errors.Is(err, ErrTreeBudget) {
		t.Fatalf("want ErrTreeBudget from token cap, got %v", err)
	}
	if n := len(stub.Requests()); n != 3 {
		t.Fatalf("token budget should have allowed 3 steps (30+30+30=90 crossed), got %d", n)
	}
}

// TestTreeBudget_SharedAcrossSubAgents asserts the budget is AGGREGATE: a
// parent's steps and a sub-agent's steps draw from ONE shared counter, so the
// total across the tree hits the cap — not one cap per Runner. With a 3-step
// budget, the parent's step 1 (delegate) + the child's steps 2 & 3 exhaust it;
// then the parent cannot continue either.
func TestTreeBudget_SharedAcrossSubAgents(t *testing.T) {
	// child spins forever if allowed; the shared budget is what stops it.
	childTurns := make([]StubTurn, 10)
	for i := range childTurns {
		childTurns[i] = spinTurn()
	}
	childStub := NewStubProvider(childTurns...)
	child, _ := NewRunner(RunnerConfig{Provider: childStub, Tools: noopSource(t), MaxSteps: 20})
	as, _ := NewAgentSource(AgentSourceConfig{Name: "worker", Description: "delegates", Runner: child})

	src := NewMultiSource()
	_ = src.Add("sub", as)
	parent := NewStubProvider(
		StubTurn{ToolCalls: []ToolCall{{ID: "d1", Name: "worker", Args: core.NewRawJSON(json.RawMessage(`{"task":"spin"}`))}}},
		StubTurn{Text: "unreachable — budget is gone"},
	)
	parentStub := parent
	r, _ := NewRunner(RunnerConfig{Provider: parent, Tools: src, MaxSteps: 20, TreeBudget: TreeBudget{MaxSteps: 3}})

	_, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "go"}}, nil)
	if !errors.Is(err, ErrTreeBudget) {
		t.Fatalf("tree budget shared across parent+child should exhaust, got %v", err)
	}
	// One shared counter of 3: parent made 1 model call, child made 2 (total 3).
	// A per-Runner budget would let each run 3, and neither would exhaust.
	if pn, cn := len(parentStub.Requests()), len(childStub.Requests()); pn != 1 || cn != 2 {
		t.Fatalf("aggregate step count wrong: parent=%d child=%d, want 1+2=3 shared", pn, cn)
	}
}
