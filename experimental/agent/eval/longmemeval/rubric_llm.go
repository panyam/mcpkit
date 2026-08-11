//go:build eval_llm

package longmemeval

import (
	"github.com/panyam/mcpkit/experimental/agent"
	"github.com/panyam/mcpkit/experimental/agent/eval"
)

// appendRubric adds the LLM-as-judge scorer for a case that has a rubric, for
// the fuzzy categories where a substring check cannot express the grade.
func appendRubric(scorers []eval.Scorer, p agent.Provider, rubric string) []eval.Scorer {
	if rubric == "" || p == nil {
		return scorers
	}
	return append(scorers, eval.Judge(p, rubric))
}
