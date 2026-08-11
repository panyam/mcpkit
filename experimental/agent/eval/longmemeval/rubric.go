//go:build !eval_llm

package longmemeval

import (
	"github.com/panyam/mcpkit/experimental/agent"
	"github.com/panyam/mcpkit/experimental/agent/eval"
)

// appendRubric marks a rubric case as ungraded in the default build.
//
// eval.Judge needs a live model and lives behind the eval_llm tag, so the
// rubric cannot be evaluated here. Returning the deterministic scorers alone
// would be worse than it looks: for a case whose real grade IS the rubric, the
// remaining scorers can be satisfied by any answer at all. The abstention case
// is exactly that shape — its only deterministic check is that one unrelated
// token is absent, so a model replying with nonsense scores as having
// correctly abstained.
//
// Suite.Run already treats a case with no scorers as a failure, on the
// reasoning that a case nothing grades passes forever. This is the same
// reasoning one step in: a case graded only by checks that cannot fail is not
// graded either. So it fails, legibly, naming the tag that would grade it.
func appendRubric(scorers []eval.Scorer, _ agent.Provider, rubric string) []eval.Scorer {
	if rubric == "" {
		return scorers
	}
	return append(scorers, eval.Ungradeable("Rubric",
		"this case is graded by an LLM rubric; run with -tags eval_llm and a live provider"))
}
