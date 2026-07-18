// Package longmemeval adapts a small, hand-written slice of long-term chat
// memory scenarios, in the spirit of the LongMemEval benchmark, into the
// agent/eval harness. It is the external quality yardstick for the Phase 2
// memory work: compaction and recall fail silently (a missing or stale
// memory yields a subtly worse answer, not an error), so a borrowed task set
// graded by a model is the only honest measure that memory actually helps.
//
// These scenarios are hand-authored in the categories LongMemEval defines
// (information extraction, multi-session reasoning, knowledge updates,
// temporal reasoning, abstention). The upstream dataset is NOT vendored —
// see ATTRIBUTION.md. The case table and its deterministic assertions live
// in the default build so they are unit-testable for well-formedness;
// running them against a live model (with an LLM judge for the fuzzy
// categories) is behind the eval_llm build tag in live_test.go.
package longmemeval

import (
	"github.com/panyam/mcpkit/agent/eval"
)

// Category names the LongMemEval-style skill a case exercises.
type Category string

const (
	// CatExtraction — recall a fact stated earlier in one conversation.
	CatExtraction Category = "extraction"
	// CatMultiSession — synthesize facts stated across separate sessions.
	CatMultiSession Category = "multi-session"
	// CatKnowledgeUpdate — a fact was superseded; the latest value must win.
	CatKnowledgeUpdate Category = "knowledge-update"
	// CatTemporal — reason about the order or timing of remembered facts.
	CatTemporal Category = "temporal"
	// CatAbstention — a fact was never stated; the model must not invent one.
	CatAbstention Category = "abstention"
)

// memoryInstructions steer the model to actually use its scratchpad — the
// point of the benchmark is the memory tools, not the context window.
const memoryInstructions = "You have a working memory with remember, recall, and forget tools. " +
	"Save durable facts the user tells you, and recall them before answering questions about earlier turns. " +
	"If you were never told something, say you do not know rather than guessing."

// MemCase is one memory scenario plus how to grade its final answer:
// deterministic substring checks (Must / MustNot) where the expected token
// is unambiguous, and a Rubric for the LLM judge where it is not.
type MemCase struct {
	Category Category
	Scenario eval.Scenario
	// Rubric grades the final answer via the LLM judge (fuzzy categories).
	// Empty skips the judge.
	Rubric string
	// Must requires each substring in the final answer (deterministic).
	Must []string
	// MustNot forbids each substring in the final answer (deterministic) —
	// the supersede and abstention checks.
	MustNot []string
}

// Cases returns the adapted scenario set, one per category as a first slice.
// Each is memory-enabled so the model manages the fact through its tools.
func Cases() []MemCase {
	scenario := func(name string, turns ...string) eval.Scenario {
		return eval.Scenario{Name: name, Turns: turns, Memory: true, Instructions: memoryInstructions}
	}
	return []MemCase{
		{
			Category: CatExtraction,
			Scenario: scenario("extraction-workplace",
				"My name is Ada and I work at Analytical Engines Inc.",
				"By the way, my primary language is Go.",
				"Where do I work?"),
			Must:   []string{"Analytical Engines"},
			Rubric: "The answer must state that the user works at Analytical Engines Inc.",
		},
		{
			Category: CatMultiSession,
			Scenario: scenario("multisession-dog-breed",
				"[Session 1] I adopted a dog last month and named her Pixel.",
				"[Session 2, weeks later] Pixel turned out to be a border collie.",
				"What breed is my dog?"),
			Must:   []string{"border collie"},
			Rubric: "The answer must identify the user's dog Pixel as a border collie, joining facts from two sessions.",
		},
		{
			Category: CatKnowledgeUpdate,
			Scenario: scenario("update-editor",
				"My favorite code editor is Sublime Text.",
				"Actually, I switched to VS Code last week and love it.",
				"Which editor do I use now?"),
			Must:    []string{"VS Code"},
			MustNot: []string{"Sublime"},
			Rubric:  "The answer must say VS Code (the updated value) and must not claim the user still uses Sublime Text.",
		},
		{
			Category: CatTemporal,
			Scenario: scenario("temporal-cities",
				"In 2019 I was living in Berlin.",
				"In 2022 I moved to Tokyo.",
				"Which of those two cities did I live in earliest?"),
			Must:   []string{"Berlin"},
			Rubric: "The answer must say Berlin, reasoning that 2019 is earlier than 2022.",
		},
		{
			Category: CatAbstention,
			Scenario: scenario("abstention-sister",
				"My name is Ada and I have a younger sister.",
				"What is my sister's name?"),
			MustNot: []string{"Analytical"}, // must not bleed an unrelated remembered token
			Rubric:  "The user never gave their sister's name. The answer must acknowledge it does not know and must NOT invent a name.",
		},
	}
}

// Deterministic returns the substring scorers for a case (Must → Contains,
// MustNot → NotContains). These run without a model, so they are the CI-safe
// regression layer; the LLM judge (Rubric) is the periodic quality layer.
func (c MemCase) Deterministic() []eval.Scorer {
	var out []eval.Scorer
	for _, s := range c.Must {
		out = append(out, eval.Contains(s))
	}
	for _, s := range c.MustNot {
		out = append(out, eval.NotContains(s))
	}
	return out
}
