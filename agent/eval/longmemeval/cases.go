// Package longmemeval provides fast, self-contained SMOKE scenarios for the
// Phase 2 memory work, shaped after the LongMemEval benchmark's skill
// categories. These are NOT the LongMemEval benchmark: they are a handful of
// short, hand-authored conversations (2–5 turns), not the dataset's long
// (~100k-token) histories, so they exercise the memory plumbing end-to-end
// with a real model and no download — a regression signal, not a
// published-comparable score.
//
// Why a smoke layer at all: memory fails silently (a missing or stale memory
// yields a subtly worse answer, not an error), so even a coarse model-graded
// check is worth having in a form that runs with zero external data.
//
// The rigorous bar — the actual LongMemEval dataset adapted through this same
// agent/eval harness — is a follow-up loader that reads the downloaded
// dataset from an env path (the dataset is NOT vendored; see ATTRIBUTION.md),
// the same convention the conformance suites use for external test data. That
// loader, plus a general eval/benchmark adapter seam so the harness can point
// at many external suites, is where comparable numbers come from.
//
// The scenario table and its deterministic assertions live in the default
// build so they are unit-testable for well-formedness; running them against a
// live model (with an LLM judge for the fuzzy categories) is behind the
// eval_llm build tag in live_test.go.
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
	// CatCompaction — an early fact must survive history compaction: the run
	// uses a low compaction budget so the head is summarized, and the answer
	// still depends on a detail from the summarized region.
	CatCompaction Category = "compaction"
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

	// CompactTokens, when > 0, runs the scenario under a SummarizingCompactor
	// with this token budget, so the early turns are summarized before the
	// final question. It is how a CatCompaction case proves a fact survives
	// the compaction boundary. Zero runs with no compactor.
	CompactTokens int
}

// SmokeScenarios returns the illustrative smoke set, one short scenario per
// category. Each is memory-enabled so the model manages the fact through its
// tools. These are a plumbing regression signal, not the LongMemEval
// benchmark — see the package doc.
func SmokeScenarios() []MemCase {
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
		{
			Category: CatCompaction,
			Scenario: eval.Scenario{
				Name:         "compaction-employee-id",
				Instructions: memoryInstructions,
				// The ID is stated first, then buried under filler chatter so a
				// low compaction budget summarizes it out of the raw transcript;
				// the summarizer must carry it forward for the final answer.
				Turns: []string{
					"Please note my employee ID is 4471 for anything work-related.",
					"Also I like hiking on weekends and cold brew coffee.",
					"My commute is about 40 minutes each way on the train.",
					"I have a standup every morning at 9:30.",
					"What is my employee ID?",
				},
			},
			Must:          []string{"4471"},
			Rubric:        "The answer must give the employee ID 4471, recalled from the earlier (compacted) part of the conversation.",
			CompactTokens: 40,
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
