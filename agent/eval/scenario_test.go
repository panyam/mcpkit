package eval

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/core"
)

// TestRunScenarioThreadsMemoryAcrossTurns is the deterministic plumbing
// test for the multi-turn harness: a fact remembered on turn 1 is recalled
// and answered on turn 2, proving history + the shared MemorySource thread
// across turns without a live model.
func TestRunScenarioThreadsMemoryAcrossTurns(t *testing.T) {
	stub := agent.NewStubProvider(
		// scenario turn 1: remember
		agent.StubTurn{ToolCalls: []agent.ToolCall{{
			ID: "c1", Name: agent.RememberToolName,
			Args: core.NewRawJSON(json.RawMessage(`{"key":"lang","value":"Go"}`)),
		}}},
		agent.StubTurn{Text: "saved"},
		// scenario turn 2: recall, then answer
		agent.StubTurn{ToolCalls: []agent.ToolCall{{
			ID: "c2", Name: agent.RecallToolName,
			Args: core.NewRawJSON(json.RawMessage(`{"query":"lang"}`)),
		}}},
		agent.StubTurn{Text: "your language is Go"},
	)

	results, err := RunScenario(context.Background(), agent.RunnerConfig{Provider: stub},
		Scenario{Name: "recall", Turns: []string{"remember my language is Go", "what is my language?"}, Memory: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d turn results, want 2", len(results))
	}

	// the graded (final) turn answers from recalled memory
	final := Final(results)
	if final.Turn == nil || !strings.Contains(final.Turn.Text, "Go") {
		t.Fatalf("final answer = %+v, want it to contain the recalled value", final.Turn)
	}

	// turn 2's first model call saw turn 1's history (threading)
	secondTurnReq := stub.Requests()[2]
	var sawRemember bool
	for _, m := range secondTurnReq.Messages {
		if m.Role == agent.RoleTool && strings.Contains(m.Text, "remembered") {
			sawRemember = true
		}
	}
	if !sawRemember {
		t.Fatal("second turn did not see the first turn's history; threading is broken")
	}
}

func TestRunScenarioReportsHarnessError(t *testing.T) {
	// no Provider -> NewRunner rejects -> harness-level error, not a Result
	_, err := RunScenario(context.Background(), agent.RunnerConfig{},
		Scenario{Name: "bad", Turns: []string{"hi"}})
	if err == nil {
		t.Fatal("expected a harness error for a missing provider")
	}
}

func TestRunScenarioWithoutMemory(t *testing.T) {
	stub := agent.NewStubProvider(
		agent.StubTurn{Text: "one"},
		agent.StubTurn{Text: "two"},
	)
	results, err := RunScenario(context.Background(), agent.RunnerConfig{Provider: stub},
		Scenario{Name: "plain", Turns: []string{"a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || Final(results).Turn.Text != "two" {
		t.Fatalf("plain scenario = %+v", results)
	}
}
