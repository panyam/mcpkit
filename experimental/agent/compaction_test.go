package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
)

func TestCharTokenEstimator(t *testing.T) {
	e := CharTokenEstimator{} // default 4 chars/token
	msgs := []Message{{Role: RoleUser, Text: strings.Repeat("a", 40)}}
	if got := e.Estimate(msgs); got != 10 {
		t.Fatalf("estimate = %d, want 10", got)
	}
	// tool-call args count toward the estimate
	msgs = append(msgs, Message{Role: RoleAssistant, ToolCalls: []ToolCall{{
		Name: "x", Args: core.NewRawJSON([]byte(strings.Repeat("b", 20))),
	}}})
	if got := e.Estimate(msgs); got != 15 {
		t.Fatalf("estimate with tool args = %d, want 15", got)
	}
}

func TestSummarizingCompactor_NoOpUnderBudget(t *testing.T) {
	stub := NewStubProvider() // would error if called — proves no summarize happens
	c, err := NewSummarizingCompactor(SummarizingConfig{Provider: stub, MaxTokens: 1000, KeepRecent: 2})
	if err != nil {
		t.Fatal(err)
	}
	history := []Message{
		{Role: RoleUser, Text: "short"},
		{Role: RoleAssistant, Text: "reply"},
	}
	out, err := c.Compact(context.Background(), history)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(history) {
		t.Fatalf("under budget should be a no-op: %d -> %d", len(history), len(out))
	}
	if len(stub.Requests()) != 0 {
		t.Fatal("summarizer must not be called under budget")
	}
}

func TestSummarizingCompactor_SummarizesHeadKeepsTail(t *testing.T) {
	stub := NewStubProvider(StubTurn{Text: "user likes Go; works at Analytical Engines"})
	c, err := NewSummarizingCompactor(SummarizingConfig{
		Provider: stub, MaxTokens: 5, KeepRecent: 2, // tiny budget forces compaction
	})
	if err != nil {
		t.Fatal(err)
	}
	history := []Message{
		{Role: RoleUser, Text: strings.Repeat("early context ", 5)},
		{Role: RoleAssistant, Text: strings.Repeat("more early ", 5)},
		{Role: RoleUser, Text: strings.Repeat("even more ", 5)},
		{Role: RoleUser, Text: "recent-A"},
		{Role: RoleAssistant, Text: "recent-B"},
	}
	out, err := c.Compact(context.Background(), history)
	if err != nil {
		t.Fatal(err)
	}
	// summary + the 2 kept tail messages
	if len(out) != 3 {
		t.Fatalf("compacted length = %d, want 3 (summary + 2 tail)", len(out))
	}
	if out[0].Role != RoleSystem || !strings.Contains(out[0].Text, "Analytical Engines") {
		t.Fatalf("head not summarized into a system message: %+v", out[0])
	}
	// the recent tail survives verbatim
	if out[1].Text != "recent-A" || out[2].Text != "recent-B" {
		t.Fatalf("tail not preserved verbatim: %+v", out[1:])
	}
}

func TestSummarizingCompactor_DoesNotOrphanToolResult(t *testing.T) {
	stub := NewStubProvider(StubTurn{Text: "summary"})
	// KeepRecent=1 would put the cut right before a RoleTool message; the
	// compactor must pull the cut earlier so the tail starts on a non-tool.
	c, _ := NewSummarizingCompactor(SummarizingConfig{Provider: stub, MaxTokens: 1, KeepRecent: 1})
	history := []Message{
		{Role: RoleUser, Text: strings.Repeat("x", 40)},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "t1", Name: "f", Args: core.NewRawJSON([]byte(`{}`))}}},
		{Role: RoleTool, ToolCallID: "t1", Text: "tool output"},
	}
	out, err := c.Compact(context.Background(), history)
	if err != nil {
		t.Fatal(err)
	}
	// tail must not begin with an orphaned RoleTool
	for _, m := range out {
		if m.Role == RoleTool {
			// it's only allowed if its assistant call is also present in out
			found := false
			for _, mm := range out {
				for _, tc := range mm.ToolCalls {
					if tc.ID == m.ToolCallID {
						found = true
					}
				}
			}
			if !found {
				t.Fatalf("orphaned RoleTool in compacted output: %+v", out)
			}
		}
	}
}

func TestSummarizingCompactor_Validation(t *testing.T) {
	if _, err := NewSummarizingCompactor(SummarizingConfig{MaxTokens: 10}); err == nil {
		t.Fatal("missing Provider should error")
	}
	if _, err := NewSummarizingCompactor(SummarizingConfig{Provider: NewStubProvider()}); err == nil {
		t.Fatal("missing MaxTokens should error")
	}
}

func TestRunnerCompactionHookFires(t *testing.T) {
	// summarizer stub returns the summary; chat stub answers after compaction.
	summarizer := NewStubProvider(StubTurn{Text: "compacted facts"})
	compactor, _ := NewSummarizingCompactor(SummarizingConfig{Provider: summarizer, MaxTokens: 5, KeepRecent: 1})

	chat := NewStubProvider(StubTurn{Text: "final answer"})
	runner, err := NewRunner(RunnerConfig{Provider: chat, Compactor: compactor})
	if err != nil {
		t.Fatal(err)
	}

	history := []Message{
		{Role: RoleUser, Text: strings.Repeat("long early context ", 5)},
		{Role: RoleAssistant, Text: strings.Repeat("long early reply ", 5)},
		{Role: RoleUser, Text: "what now?"},
	}
	var events []Event
	turn, err := runner.Run(context.Background(), history, func(e Event) { events = append(events, e) })
	if err != nil {
		t.Fatal(err)
	}
	if turn.Text != "final answer" {
		t.Fatalf("turn text = %q", turn.Text)
	}

	var comp *CompactionInfo
	for _, e := range events {
		if e.Kind == EventCompaction {
			comp = e.Compaction
		}
	}
	if comp == nil {
		t.Fatal("no EventCompaction emitted")
	}
	if comp.After >= comp.Before {
		t.Fatalf("compaction did not shrink history: %d -> %d", comp.Before, comp.After)
	}

	// the model saw the compacted (summarized) history, not the raw head
	sawSummary := false
	for _, m := range chat.Requests()[0].Messages {
		if m.Role == RoleSystem && strings.Contains(m.Text, "compacted facts") {
			sawSummary = true
		}
	}
	if !sawSummary {
		t.Fatal("model did not receive the compacted summary")
	}
}

func TestRunnerNoCompactionEventWhenNoOp(t *testing.T) {
	summarizer := NewStubProvider() // must not be called
	compactor, _ := NewSummarizingCompactor(SummarizingConfig{Provider: summarizer, MaxTokens: 100000, KeepRecent: 2})
	chat := NewStubProvider(StubTurn{Text: "ok"})
	runner, _ := NewRunner(RunnerConfig{Provider: chat, Compactor: compactor})

	var events []Event
	_, err := runner.Run(context.Background(), []Message{{Role: RoleUser, Text: "hi"}},
		func(e Event) { events = append(events, e) })
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Kind == EventCompaction {
			t.Fatal("EventCompaction should not fire on a no-op compaction")
		}
	}
}
