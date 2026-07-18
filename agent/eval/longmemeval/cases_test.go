package longmemeval

import "testing"

func TestCasesWellFormed(t *testing.T) {
	cases := Cases()
	if len(cases) == 0 {
		t.Fatal("no cases")
	}
	seen := map[string]bool{}
	cats := map[Category]bool{}
	for _, c := range cases {
		name := c.Scenario.Name
		if name == "" {
			t.Fatal("case with empty scenario name")
		}
		if seen[name] {
			t.Fatalf("duplicate scenario name %q", name)
		}
		seen[name] = true
		cats[c.Category] = true

		if len(c.Scenario.Turns) < 2 {
			t.Fatalf("%q: a memory scenario needs at least a setup turn and a question", name)
		}
		if !c.Scenario.Memory {
			t.Fatalf("%q: memory scenarios must enable Memory", name)
		}
		// every case must be gradeable: a deterministic assertion or a rubric
		if len(c.Must) == 0 && len(c.MustNot) == 0 && c.Rubric == "" {
			t.Fatalf("%q: no way to grade (no Must/MustNot and no Rubric)", name)
		}
	}

	// the first slice covers all five LongMemEval categories
	for _, want := range []Category{CatExtraction, CatMultiSession, CatKnowledgeUpdate, CatTemporal, CatAbstention} {
		if !cats[want] {
			t.Fatalf("category %q not covered", want)
		}
	}
}

func TestDeterministicScorers(t *testing.T) {
	for _, c := range Cases() {
		got := len(c.Deterministic())
		if want := len(c.Must) + len(c.MustNot); got != want {
			t.Fatalf("%q: Deterministic() = %d scorers, want %d", c.Scenario.Name, got, want)
		}
	}
}
