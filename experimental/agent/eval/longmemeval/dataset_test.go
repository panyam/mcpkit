package longmemeval

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/experimental/agent"
	"github.com/panyam/mcpkit/experimental/agent/eval"
)

const samplePath = "testdata/longmemeval_sample.json"

func TestDatasetAdapterUnconfiguredIsASkipNotAnError(t *testing.T) {
	t.Setenv(DataPathEnv, "")
	cases, err := DatasetAdapter{}.Load()
	if err != nil {
		t.Fatalf("an unset path must not error: %v", err)
	}
	if len(cases) != 0 {
		t.Fatalf("expected no cases, got %d", len(cases))
	}
}

func TestDatasetAdapterConfiguredButMissingIsAnError(t *testing.T) {
	t.Setenv(DataPathEnv, filepath.Join(t.TempDir(), "absent.json"))
	if _, err := (DatasetAdapter{}).Load(); err == nil {
		t.Fatal("a path that was set but does not exist must error, not skip: a typo would otherwise read as an unconfigured suite")
	}
}

func TestDatasetAdapterLoadsEveryInstance(t *testing.T) {
	cases, err := DatasetAdapter{Path: samplePath}.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cases) != 3 {
		t.Fatalf("expected 3 cases, got %d", len(cases))
	}

	names := make([]string, 0, len(cases))
	for _, c := range cases {
		names = append(names, c.Scenario.Name)
		if len(c.Scenario.Turns) != 1 {
			t.Errorf("%s: expected exactly the question as the graded turn, got %d turns",
				c.Scenario.Name, len(c.Scenario.Turns))
		}
		if c.Scorers == nil {
			t.Errorf("%s: no scorers", c.Scenario.Name)
		}
	}
	if !strings.Contains(strings.Join(names, ","), "temporal-reasoning/") {
		t.Errorf("case names should carry question_type: %v", names)
	}
}

// TestDatasetMemoryModeSeedsTheStoreNotTheContext pins the default mode: the
// haystack goes into a MemoryStore for the agent to retrieve, not into the
// conversation. Seeding it into context instead would silently turn every run
// into the long-context benchmark.
func TestDatasetMemoryModeSeedsTheStoreNotTheContext(t *testing.T) {
	cases, err := DatasetAdapter{Path: samplePath}.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	c := cases[0]

	if len(c.Scenario.History) != 0 {
		t.Errorf("memory mode must not seed conversation history, got %d messages", len(c.Scenario.History))
	}
	if c.Scenario.NewMemoryStore == nil {
		t.Fatal("memory mode must supply a store factory")
	}

	store, err := c.Scenario.NewMemoryStore()
	if err != nil {
		t.Fatalf("store factory: %v", err)
	}
	resp, err := store.ListMemories(context.Background(), agent.ListMemoriesRequest{})
	if err != nil {
		t.Fatalf("ListMemories: %v", err)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("expected one item per session (2), got %d", len(resp.Items))
	}
	var joined string
	for _, it := range resp.Items {
		joined += it.Item.Value
	}
	if !strings.Contains(joined, "border collie") {
		t.Errorf("the evidence turn did not reach the store: %q", joined)
	}
}

// TestDatasetMemoryStoresAreNotShared pins that each scenario gets its own
// store. A shared one would let an earlier question's haystack answer a later
// one, which scores as memory working when it is leakage.
func TestDatasetMemoryStoresAreNotShared(t *testing.T) {
	cases, err := DatasetAdapter{Path: samplePath}.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	a, err := cases[0].Scenario.NewMemoryStore()
	if err != nil {
		t.Fatal(err)
	}
	b, err := cases[1].Scenario.NewMemoryStore()
	if err != nil {
		t.Fatal(err)
	}
	has := func(s agent.MemoryStore) bool {
		got, err := s.ListMemories(context.Background(), agent.ListMemoriesRequest{Query: "border collie"})
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range got.Items {
			if strings.Contains(it.Item.Value, "border collie") {
				return true
			}
		}
		return false
	}

	if !has(a) {
		t.Error("the first question's store should hold its own haystack")
	}
	if has(b) {
		t.Error("a later question's store contained an earlier question's haystack")
	}
}

// TestDatasetLongContextModeSeedsHistory pins the opt-in mode and that roles
// survive the mapping. An assistant turn replayed as a user message rewrites
// who said the evidence.
func TestDatasetLongContextModeSeedsHistory(t *testing.T) {
	cases, err := DatasetAdapter{Path: samplePath, LongContext: true}.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	c := cases[0]

	if c.Scenario.NewMemoryStore != nil {
		t.Error("long-context mode must not also build a memory store")
	}
	if len(c.Scenario.History) != 4 {
		t.Fatalf("expected 4 seeded messages across 2 sessions, got %d", len(c.Scenario.History))
	}
	if c.Scenario.History[0].Role != agent.RoleUser || c.Scenario.History[1].Role != agent.RoleAssistant {
		t.Errorf("roles did not survive the mapping: %+v", c.Scenario.History[:2])
	}
	if !strings.Contains(c.Scenario.History[0].Text, "2023/05/02") {
		t.Errorf("the session date should prefix its first turn for temporal questions: %q", c.Scenario.History[0].Text)
	}
}

// TestDatasetQuestionDateReachesInstructions pins that "today" is supplied.
// Without it every relative date in a temporal question is unanswerable for
// reasons that have nothing to do with memory.
func TestDatasetQuestionDateReachesInstructions(t *testing.T) {
	cases, err := DatasetAdapter{Path: samplePath}.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, c := range cases {
		if !strings.Contains(c.Scenario.Instructions, "Today's date is") {
			t.Fatalf("%s: instructions carry no question date", c.Scenario.Name)
		}
	}
}

// TestDatasetAbstentionIsGradedOnTheOppositeProperty pins that an _abs
// instance is graded on declining rather than against a reference answer.
// Grading it against a reference would score a confident fabrication as
// correct whenever it happened to match.
func TestDatasetAbstentionIsGradedOnTheOppositeProperty(t *testing.T) {
	var abs Instance
	for _, inst := range loadFixture(t) {
		if inst.Abstention() {
			abs = inst
		}
	}
	if abs.QuestionID == "" {
		t.Fatal("fixture has no abstention instance")
	}
	rubric := rubricFor(abs)
	if !strings.Contains(rubric, "declines") {
		t.Errorf("abstention rubric should require a refusal: %q", rubric)
	}
	if strings.Contains(rubric, "reference answer") {
		t.Errorf("abstention rubric must not grade against a reference answer: %q", rubric)
	}
}

func TestDatasetLimit(t *testing.T) {
	t.Run("struct field", func(t *testing.T) {
		cases, err := DatasetAdapter{Path: samplePath, Limit: 2}.Load()
		if err != nil {
			t.Fatal(err)
		}
		if len(cases) != 2 {
			t.Fatalf("expected 2, got %d", len(cases))
		}
	})

	t.Run("env var", func(t *testing.T) {
		t.Setenv(LimitEnv, "1")
		cases, err := DatasetAdapter{Path: samplePath}.Load()
		if err != nil {
			t.Fatal(err)
		}
		if len(cases) != 1 {
			t.Fatalf("expected 1, got %d", len(cases))
		}
	})

	t.Run("garbage env var is an error", func(t *testing.T) {
		t.Setenv(LimitEnv, "lots")
		if _, err := (DatasetAdapter{Path: samplePath}).Load(); err == nil {
			t.Fatal("an unparseable limit should error rather than being ignored")
		}
	})
}

func TestDatasetTypeFilter(t *testing.T) {
	cases, err := DatasetAdapter{
		Path:  samplePath,
		Types: func(qt string) bool { return qt == "temporal-reasoning" },
	}.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 1 {
		t.Fatalf("expected only the temporal case, got %d", len(cases))
	}
}

// TestDatasetStrictValidation is the compensation for not being able to run
// this against the real corpus. Every case here is a shape that would
// otherwise produce a scenario that runs and grades nothing, so each must be a
// named error rather than a skipped or empty case.
func TestDatasetStrictValidation(t *testing.T) {
	base := loadFixture(t)[0]

	mutate := func(f func(*Instance)) string {
		inst := base
		f(&inst)
		b, err := json.Marshal([]Instance{inst})
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(t.TempDir(), "mutated.json")
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	cases := map[string]struct {
		mutate func(*Instance)
		want   string
	}{
		"no question_id": {func(i *Instance) { i.QuestionID = "" }, "question_id"},
		"no question":    {func(i *Instance) { i.Question = "" }, "question"},
		"no answer":      {func(i *Instance) { i.Answer = "" }, "answer"},
		"no sessions":    {func(i *Instance) { i.HaystackSessions = nil }, "haystack_sessions"},
		// Trim the parallel arrays too, or the length checks fire first and
		// this asserts a different rule than its name claims.
		"empty sessions": {func(i *Instance) {
			i.HaystackSessions = [][]Turn{{}}
			i.HaystackDates = i.HaystackDates[:1]
			i.HaystackSessionIDs = i.HaystackSessionIDs[:1]
		}, "no turns"},
		"unknown role":    {func(i *Instance) { i.HaystackSessions[0][0].Role = "system" }, "unknown role"},
		"date count skew": {func(i *Instance) { i.HaystackDates = []string{"only-one"} }, "haystack_dates"},
		"id count skew":   {func(i *Instance) { i.HaystackSessionIDs = []string{"only-one"} }, "haystack_session_ids"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := DatasetAdapter{Path: mutate(tc.mutate)}.Load()
			if err == nil {
				t.Fatalf("expected a named error, got none")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error should name %q, got %v", tc.want, err)
			}
		})
	}
}

// TestDatasetAbstentionMayOmitAnswer pins the one exemption to the above: an
// abstention instance has nothing to answer with, so an empty answer is
// meaningful rather than malformed.
func TestDatasetAbstentionMayOmitAnswer(t *testing.T) {
	cases, err := DatasetAdapter{Path: samplePath}.Load()
	if err != nil {
		t.Fatalf("the fixture's abstention instance has an empty answer and must load: %v", err)
	}
	if len(cases) != 3 {
		t.Fatalf("expected all 3 instances, got %d", len(cases))
	}
}

func TestDatasetRejectsNonArrayJSON(t *testing.T) {
	p := filepath.Join(t.TempDir(), "object.json")
	if err := os.WriteFile(p, []byte(`{"question_id":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := DatasetAdapter{Path: p}.Load()
	if err == nil || !strings.Contains(err.Error(), "JSON array") {
		t.Fatalf("expected an error naming the expected shape, got %v", err)
	}
}

// TestDatasetRunsThroughSuite is the end of the seam: the adapter's cases go
// straight into eval.Suite with no glue.
func TestDatasetRunsThroughSuite(t *testing.T) {
	cases, err := DatasetAdapter{Path: samplePath}.Load()
	if err != nil {
		t.Fatal(err)
	}
	turns := make([]agent.StubTurn, 0, 16)
	for range cap(turns) {
		turns = append(turns, agent.StubTurn{Text: "stub answer"})
	}
	report := eval.Suite{
		Config: agent.RunnerConfig{Provider: agent.NewStubProvider(turns...)},
		Cases:  cases,
	}.Run(context.Background())

	if report.Total != len(cases) {
		t.Fatalf("suite ran %d of %d", report.Total, len(cases))
	}
	// Every case is rubric-graded, so without the eval_llm tag every one is
	// Ungradeable rather than passing on a stub answer.
	if report.Passed != 0 {
		t.Errorf("no case should pass untagged:\n%s", report)
	}
	for _, c := range report.Cases {
		if c.RunErr != "" {
			t.Errorf("%s failed to run: %s", c.Case, c.RunErr)
		}
	}
}

func loadFixture(t *testing.T) []Instance {
	t.Helper()
	raw, err := os.ReadFile(samplePath)
	if err != nil {
		t.Fatal(err)
	}
	var out []Instance
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
