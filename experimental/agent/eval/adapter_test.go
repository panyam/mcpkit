package eval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/experimental/agent"
)

// TestSuitePerCaseScorers covers the thing the old Suite could not express:
// two cases in one suite, each graded against its own expected answer.
//
// Under the previous shape (one Scorers list for the whole suite) the only way
// to write this was a scorer that passed for both answers, which grades
// nothing. That limitation is why longmemeval hand-rolled its own loop.
func TestSuitePerCaseScorers(t *testing.T) {
	suite := Suite{
		Config: agent.RunnerConfig{
			Provider: agent.NewStubProvider(
				agent.StubTurn{Text: "paris"},
				agent.StubTurn{Text: "rome"},
			),
		},
		Cases: []SuiteCase{
			Single(Case{Name: "france", Input: "capital of france"}, ExactMatch("paris")),
			Single(Case{Name: "italy", Input: "capital of italy"}, ExactMatch("rome")),
		},
	}

	report := suite.Run(context.Background())
	if report.Passed != 2 || report.Failed != 0 {
		t.Fatalf("both cases should pass against their own ground truth: %s", report)
	}
}

// TestSuitePerCaseScorersCatchTheWrongAnswer is the negative half: the same
// two cases with the answers swapped must both fail, proving each case's
// scorers really are bound to that case rather than being satisfied by any
// case's answer.
func TestSuitePerCaseScorersCatchTheWrongAnswer(t *testing.T) {
	suite := Suite{
		Config: agent.RunnerConfig{
			Provider: agent.NewStubProvider(
				agent.StubTurn{Text: "rome"},
				agent.StubTurn{Text: "paris"},
			),
		},
		Cases: []SuiteCase{
			Single(Case{Name: "france", Input: "capital of france"}, ExactMatch("paris")),
			Single(Case{Name: "italy", Input: "capital of italy"}, ExactMatch("rome")),
		},
	}

	report := suite.Run(context.Background())
	if report.Failed != 2 {
		t.Fatalf("swapped answers should fail both cases: %s", report)
	}
}

// TestSuiteCaseConfigureIsPerCase pins that a case's config change does not
// leak into the next case. Configure returns a config rather than mutating
// one for exactly this reason.
func TestSuiteCaseConfigureIsPerCase(t *testing.T) {
	// The second case observes the config it is handed. If the first case's
	// override leaked, it arrives here as 7 instead of the suite's 3.
	//
	// Observing it through Configure is deliberate: Result.Case carries only
	// the turn's name and input, so asserting on the Result would compare two
	// zero values and pass whether or not the leak existed.
	var handed int
	suite := Suite{
		Config: agent.RunnerConfig{
			Provider: agent.NewStubProvider(agent.StubTurn{Text: "a"}, agent.StubTurn{Text: "b"}),
			MaxSteps: 3,
		},
		Cases: []SuiteCase{
			{
				Scenario: Scenario{Name: "with-override", Turns: []string{"hi"}},
				Scorers:  func(agent.Provider) []Scorer { return []Scorer{NoError()} },
				Configure: func(cfg agent.RunnerConfig) (agent.RunnerConfig, error) {
					cfg.MaxSteps = 7
					return cfg, nil
				},
			},
			{
				Scenario: Scenario{Name: "observes", Turns: []string{"hi"}},
				Scorers:  func(agent.Provider) []Scorer { return []Scorer{NoError()} },
				Configure: func(cfg agent.RunnerConfig) (agent.RunnerConfig, error) {
					handed = cfg.MaxSteps
					return cfg, nil
				},
			},
		},
	}

	report := suite.Run(context.Background())
	if report.Failed != 0 {
		t.Fatalf("both cases should pass: %s", report)
	}
	if handed != 3 {
		t.Fatalf("the first case's override leaked into the second: MaxSteps = %d, want the suite's 3", handed)
	}
}

// TestSuiteCaseConfigureErrorIsPerCase pins that a Configure failure fails
// only its own case and the suite keeps going.
func TestSuiteCaseConfigureErrorIsPerCase(t *testing.T) {
	suite := Suite{
		Config: agent.RunnerConfig{Provider: agent.NewStubProvider(agent.StubTurn{Text: "ok"})},
		Cases: []SuiteCase{
			{
				Scenario:  Scenario{Name: "broken", Turns: []string{"hi"}},
				Scorers:   func(agent.Provider) []Scorer { return []Scorer{NoError()} },
				Configure: func(agent.RunnerConfig) (agent.RunnerConfig, error) { return agent.RunnerConfig{}, errors.New("boom") },
			},
			Single(Case{Name: "fine", Input: "hi"}, NoError()),
		},
	}

	report := suite.Run(context.Background())
	if report.Total != 2 || report.Passed != 1 || report.Failed != 1 {
		t.Fatalf("one case should fail on configure, the other run: %s", report)
	}
	if got := report.Cases[0].RunErr; !strings.Contains(got, "boom") {
		t.Errorf("first case should carry the configure error, got %q", got)
	}
}

// TestSuiteCaseWithNoTurnsIsReported pins that a scenario with seeded History
// and no Turns is reported as that case's failure rather than panicking the
// whole suite. Final panics on an empty slice, and forgetting the question
// while populating the history is a plausible adapter bug.
func TestSuiteCaseWithNoTurnsIsReported(t *testing.T) {
	suite := Suite{
		Config: agent.RunnerConfig{Provider: agent.NewStubProvider(agent.StubTurn{Text: "ok"})},
		Cases: []SuiteCase{{
			Scenario: Scenario{
				Name:    "history-only",
				History: []agent.Message{{Role: agent.RoleUser, Text: "prior"}},
			},
			Scorers: func(agent.Provider) []Scorer { return []Scorer{NoError()} },
		}},
	}

	report := suite.Run(context.Background())
	if report.Failed != 1 {
		t.Fatalf("a turnless case should fail: %s", report)
	}
	if got := report.Cases[0].RunErr; !strings.Contains(got, "no turns") {
		t.Errorf("expected a 'ran no turns' error, got %q", got)
	}
}

// TestScenarioHistoryIsSeededNotRun pins the distinction the History field
// exists for: seeded messages reach the model as context, without the model
// being called to produce them.
func TestScenarioHistoryIsSeededNotRun(t *testing.T) {
	stub := agent.NewStubProvider(agent.StubTurn{Text: "answer"})
	s := Scenario{
		Name: "seeded",
		History: []agent.Message{
			{Role: agent.RoleUser, Text: "my cat is called Mango"},
			{Role: agent.RoleAssistant, Text: "noted"},
		},
		Turns: []string{"what is my cat called?"},
	}

	results, err := RunScenario(context.Background(), agent.RunnerConfig{Provider: stub}, s)
	if err != nil {
		t.Fatalf("RunScenario: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("two history messages must not become turns; got %d results", len(results))
	}

	reqs := stub.Requests()
	if len(reqs) != 1 {
		t.Fatalf("the model should be called once, for the single turn; got %d calls", len(reqs))
	}
	var texts []string
	for _, m := range reqs[0].Messages {
		texts = append(texts, m.Text)
	}
	joined := strings.Join(texts, "|")
	if !strings.Contains(joined, "Mango") {
		t.Errorf("seeded history did not reach the model: %q", joined)
	}
}

// TestScenarioHistoryIsNotAliased pins that running a Scenario twice does not
// accumulate the first run's turns into the caller's History slice. A suite
// that grades one scenario across several memory backends does exactly that.
func TestScenarioHistoryIsNotAliased(t *testing.T) {
	s := Scenario{
		Name:    "reused",
		History: []agent.Message{{Role: agent.RoleUser, Text: "seed"}},
		Turns:   []string{"go"},
	}
	for i := 0; i < 2; i++ {
		stub := agent.NewStubProvider(agent.StubTurn{Text: "ok"})
		if _, err := RunScenario(context.Background(), agent.RunnerConfig{Provider: stub}, s); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if len(s.History) != 1 {
		t.Fatalf("the scenario's History grew across runs: %+v", s.History)
	}
}

// TestSuitePath covers the three outcomes an adapter has to tell apart:
// configured and present, not configured, and configured but wrong.
func TestSuitePath(t *testing.T) {
	const env = "MCPKIT_TEST_SUITE_PATH"

	t.Run("unset is not an error", func(t *testing.T) {
		t.Setenv(env, "")
		path, ok, err := SuitePath(env)
		if err != nil || ok || path != "" {
			t.Fatalf("got (%q, %v, %v)", path, ok, err)
		}
	})

	t.Run("present resolves", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "suite.json")
		if err := os.WriteFile(f, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv(env, f)
		path, ok, err := SuitePath(env)
		if err != nil || !ok || path != f {
			t.Fatalf("got (%q, %v, %v)", path, ok, err)
		}
	})

	t.Run("set but missing is an error, not a skip", func(t *testing.T) {
		t.Setenv(env, filepath.Join(t.TempDir(), "nope.json"))
		_, ok, err := SuitePath(env)
		if err == nil {
			t.Fatal("a typo'd path must be an error; reporting it as unconfigured hides a suite that never runs")
		}
		if ok {
			t.Fatal("ok should be false on error")
		}
	})
}

// stubAdapter is a minimal Adapter for LoadSuite's contract.
type stubAdapter struct {
	name  string
	cases []SuiteCase
	err   error
}

func (a stubAdapter) Name() string               { return a.name }
func (a stubAdapter) Load() ([]SuiteCase, error) { return a.cases, a.err }

// TestLoadSuite pins that an unconfigured adapter is a skip rather than an
// empty pass, and that a load failure is an error.
func TestLoadSuite(t *testing.T) {
	cfg := agent.RunnerConfig{Provider: agent.NewStubProvider()}

	if _, ok, err := LoadSuite(cfg, stubAdapter{name: "empty"}); ok || err != nil {
		t.Fatalf("no cases should report not-configured: ok=%v err=%v", ok, err)
	}

	if _, _, err := LoadSuite(cfg, stubAdapter{name: "bad", err: errors.New("corrupt")}); err == nil {
		t.Fatal("a load failure must surface as an error")
	}

	suite, ok, err := LoadSuite(cfg, stubAdapter{
		name:  "one",
		cases: []SuiteCase{Single(Case{Name: "c"}, NoError())},
	})
	if err != nil || !ok || len(suite.Cases) != 1 {
		t.Fatalf("got (%d cases, %v, %v)", len(suite.Cases), ok, err)
	}
}

// TestDimensionsAggregatesByScorerName covers the rollup #1060 needed and
// #1015 deliberately deferred: two independent dimensions counted separately
// rather than collapsed into one case verdict.
func TestDimensionsAggregatesByScorerName(t *testing.T) {
	report := SuiteReport{Cases: []CaseReport{
		{Case: "a", Scores: []Score{{Name: "utility", Pass: true}, {Name: "security", Pass: false}}},
		{Case: "b", Scores: []Score{{Name: "utility", Pass: true}, {Name: "security", Pass: true}}},
		{Case: "c", Scores: []Score{{Name: "utility", Pass: false}, {Name: "security", Pass: true}}},
	}}

	dims := report.Dimensions()
	if len(dims) != 2 {
		t.Fatalf("expected 2 dimensions, got %+v", dims)
	}
	// First-seen order, so a report reads in the order the scorers ran.
	if dims[0].Name != "utility" || dims[1].Name != "security" {
		t.Errorf("dimensions should keep first-seen order, got %q then %q", dims[0].Name, dims[1].Name)
	}
	if dims[0].Passed != 2 || dims[0].Failed != 1 || dims[0].Total != 3 {
		t.Errorf("utility = %+v", dims[0])
	}
	if dims[1].Passed != 2 || dims[1].Failed != 1 || dims[1].Total != 3 {
		t.Errorf("security = %+v", dims[1])
	}
	if got := dims[0].Rate(); got < 0.66 || got > 0.67 {
		t.Errorf("utility rate = %v, want ~0.667", got)
	}
}

// TestDimensionsHandlesPartialGrading pins that a dimension only some cases
// carry reports a smaller Total rather than counting ungraded cases as failures.
func TestDimensionsHandlesPartialGrading(t *testing.T) {
	report := SuiteReport{Cases: []CaseReport{
		{Case: "a", Scores: []Score{{Name: "utility", Pass: true}, {Name: "security", Pass: true}}},
		{Case: "b", Scores: []Score{{Name: "utility", Pass: true}}},
		{Case: "c", RunErr: "boom"},
	}}

	dims := report.Dimensions()
	byName := map[string]DimensionReport{}
	for _, d := range dims {
		byName[d.Name] = d
	}
	if got := byName["utility"].Total; got != 2 {
		t.Errorf("utility Total = %d, want 2", got)
	}
	if got := byName["security"].Total; got != 1 {
		t.Errorf("security Total = %d, want 1: a case that did not carry the dimension is not a failure of it", got)
	}
}

// TestDimensionsEmpty pins the zero cases.
func TestDimensionsEmpty(t *testing.T) {
	if got := (SuiteReport{}).Dimensions(); len(got) != 0 {
		t.Errorf("expected no dimensions, got %+v", got)
	}
	if got := (DimensionReport{}).Rate(); got != 0 {
		t.Errorf("an ungraded dimension has rate 0, not a divide by zero: %v", got)
	}
}
