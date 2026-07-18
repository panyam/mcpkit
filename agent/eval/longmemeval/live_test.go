//go:build eval_llm

// Live benchmark: runs the adapted LongMemEval scenarios against a real
// model and grades them (deterministic substring checks plus an LLM judge
// for the fuzzy categories). It is excluded from the default build and CI —
// it needs a live provider — and reports a pass rate rather than gating,
// since memory quality is statistical. Run with:
//
//	LONGMEMEVAL_BASE_URL=http://localhost:1234/v1 LONGMEMEVAL_MODEL=your-model \
//	  go test -tags eval_llm ./agent/eval/longmemeval/ -run TestLive -v
package longmemeval

import (
	"context"
	"os"
	"testing"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/agent/eval"
)

// liveProvider builds a provider from the LONGMEMEVAL_* env, skipping the
// test when no endpoint is configured so a bare `go test -tags eval_llm`
// stays green.
func liveProvider(t *testing.T) agent.Provider {
	baseURL := os.Getenv("LONGMEMEVAL_BASE_URL")
	model := os.Getenv("LONGMEMEVAL_MODEL")
	if baseURL == "" || model == "" {
		t.Skip("set LONGMEMEVAL_BASE_URL and LONGMEMEVAL_MODEL to run the live memory benchmark")
	}
	p, err := agent.NewOpenAIProvider(agent.OpenAIConfig{
		BaseURL: baseURL,
		Model:   model,
		APIKey:  os.Getenv(os.Getenv("LONGMEMEVAL_API_KEY_ENV")),
	})
	if err != nil {
		t.Fatalf("build provider: %v", err)
	}
	return p
}

func TestLiveLongMemEval(t *testing.T) {
	provider := liveProvider(t)
	ctx := context.Background()

	var passed, total int
	for _, c := range SmokeScenarios() {
		cfg := agent.RunnerConfig{Provider: provider}
		if c.CompactTokens > 0 {
			// The compaction case runs under a low budget so the early turns
			// are summarized before the final question — this is issue 939's
			// SummarizingCompactor graded by issue 974's harness.
			compactor, err := agent.NewSummarizingCompactor(agent.SummarizingConfig{
				Provider: provider, MaxTokens: c.CompactTokens,
			})
			if err != nil {
				t.Fatalf("%s: build compactor: %v", c.Scenario.Name, err)
			}
			cfg.Compactor = compactor
		}
		results, err := eval.RunScenario(ctx, cfg, c.Scenario)
		if err != nil {
			t.Fatalf("%s: harness error: %v", c.Scenario.Name, err)
		}
		final := eval.Final(results)

		casePass := true
		for _, s := range c.Deterministic() {
			if sc := s.Score(final); !sc.Pass {
				casePass = false
				t.Logf("[%s] %s FAIL: %s", c.Category, c.Scenario.Name, sc.Detail)
			}
		}
		if c.Rubric != "" {
			if sc := eval.Judge(provider, c.Rubric).Score(final); !sc.Pass {
				casePass = false
				t.Logf("[%s] %s JUDGE FAIL (%.2f): %s", c.Category, c.Scenario.Name, sc.Value, sc.Detail)
			}
		}

		total++
		if casePass {
			passed++
			t.Logf("[%s] %s PASS", c.Category, c.Scenario.Name)
		}
	}
	// Report, do not gate — the benchmark's job is to surface the pass rate.
	t.Logf("LongMemEval-derived pass rate: %d/%d", passed, total)
}
