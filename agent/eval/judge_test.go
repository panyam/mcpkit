//go:build eval_llm

package eval

import (
	"testing"

	"github.com/panyam/mcpkit/agent"
)

// TestJudgeParsesVerdict drives the LLM-judge scorer with a StubProvider
// scripted to return a JSON verdict, so the build-tagged path stays testable
// without a live model. Run with: go test -tags eval_llm ./...
func TestJudgeParsesVerdict(t *testing.T) {
	stub := agent.NewStubProvider(agent.StubTurn{
		Text: `{"pass": true, "score": 0.9, "reason": "answer matches the rubric"}`,
	})
	res := runCase(t, nil, Case{Name: "judge", Input: "hi"}, agent.StubTurn{Text: "Hello there"})
	// Re-point Run's result at a fresh transcript is unnecessary; grade it.
	s := Judge(stub, "The answer must greet the user.").Score(res)
	if !s.Pass || s.Value != 0.9 || s.Detail != "answer matches the rubric" {
		t.Fatalf("verdict = %+v", s)
	}
}

func TestJudgeFailsOnBadJSON(t *testing.T) {
	stub := agent.NewStubProvider(agent.StubTurn{Text: "not json"})
	res := Result{Turn: &agent.TurnResult{Text: "whatever"}}
	if s := Judge(stub, "rubric").Score(res); s.Pass {
		t.Fatalf("non-JSON verdict should fail: %+v", s)
	}
}

func TestJudgeFailsOnProviderError(t *testing.T) {
	stub := agent.NewStubProvider() // exhausted immediately -> Generate errors
	res := Result{Turn: &agent.TurnResult{Text: "whatever"}}
	if s := Judge(stub, "rubric").Score(res); s.Pass {
		t.Fatalf("provider error should fail: %+v", s)
	}
}
