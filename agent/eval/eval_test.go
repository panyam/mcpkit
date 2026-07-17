package eval

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/core"
)

// lookupSource builds a FuncSource with one "lookup" tool that echoes its key,
// plus an always-failing "boom" tool for the error paths.
func lookupSource(t *testing.T) *agent.FuncSource {
	t.Helper()
	src := agent.NewFuncSource()
	if err := agent.AddFunc(src, "lookup", "looks up a key", func(ctx context.Context, in struct {
		Key string `json:"key"`
	}) (string, error) {
		return "value-for-" + in.Key, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := agent.AddFunc(src, "boom", "always fails", func(ctx context.Context, _ struct{}) (string, error) {
		return "", errors.New("kaput")
	}); err != nil {
		t.Fatal(err)
	}
	return src
}

func toolCallTurn(id, name, args string) agent.StubTurn {
	return agent.StubTurn{ToolCalls: []agent.ToolCall{
		{ID: id, Name: name, Args: core.NewRawJSON(json.RawMessage(args))},
	}}
}

// runCase is a small helper: build a stub-backed config and run one case.
func runCase(t *testing.T, src agent.ToolSource, c Case, turns ...agent.StubTurn) Result {
	t.Helper()
	cfg := agent.RunnerConfig{Provider: agent.NewStubProvider(turns...), Tools: src}
	res, err := Run(context.Background(), cfg, c)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func TestRunSeedsInputAndCapturesTranscript(t *testing.T) {
	res := runCase(t, nil, Case{Name: "greet", Input: "hi"},
		agent.StubTurn{Text: "Hello there"})
	if res.Err != nil {
		t.Fatalf("run error: %v", res.Err)
	}
	if res.Turn == nil || res.Turn.Text != "Hello there" {
		t.Fatalf("turn = %+v", res.Turn)
	}
	if len(res.Events) == 0 || res.Events[0].Kind != agent.EventTurnBegin {
		t.Fatalf("events not captured: %+v", res.Events)
	}
}

func TestRunHistoryOverridesInput(t *testing.T) {
	cfg := agent.RunnerConfig{Provider: agent.NewStubProvider(agent.StubTurn{Text: "ok"})}
	c := Case{
		Name:    "hist",
		Input:   "ignored",
		History: []agent.Message{{Role: agent.RoleUser, Text: "from history"}},
	}
	if _, err := Run(context.Background(), cfg, c); err != nil {
		t.Fatal(err)
	}
	// Provider recorded the seeded history verbatim.
	stub := cfg.Provider.(*agent.StubProvider)
	got := stub.Requests()[0].Messages[0].Text
	if got != "from history" {
		t.Fatalf("history not used: %q", got)
	}
}

func TestRunReportsHarnessErrorForMissingProvider(t *testing.T) {
	_, err := Run(context.Background(), agent.RunnerConfig{}, Case{Name: "x"})
	if err == nil {
		t.Fatal("want harness error for missing provider")
	}
}

func TestExactMatch(t *testing.T) {
	res := runCase(t, nil, Case{Name: "em"}, agent.StubTurn{Text: "42"})
	if s := ExactMatch("42").Score(res); !s.Pass || s.Value != 1.0 {
		t.Fatalf("pass case: %+v", s)
	}
	if s := ExactMatch("43").Score(res); s.Pass {
		t.Fatalf("fail case should not pass: %+v", s)
	}
}

func TestContains(t *testing.T) {
	res := runCase(t, nil, Case{Name: "c"}, agent.StubTurn{Text: "the answer is 42 today"})
	if s := Contains("answer is 42").Score(res); !s.Pass {
		t.Fatalf("pass case: %+v", s)
	}
	if s := Contains("nope").Score(res); s.Pass {
		t.Fatalf("fail case should not pass: %+v", s)
	}
}

func TestToolCalledTrueAndFalse(t *testing.T) {
	src := lookupSource(t)
	res := runCase(t, src, Case{Name: "tc", Input: "get x"},
		toolCallTurn("c1", "lookup", `{"key":"x"}`),
		agent.StubTurn{Text: "value-for-x"},
	)
	if s := ToolCalled("lookup").Score(res); !s.Pass {
		t.Fatalf("lookup was called: %+v", s)
	}
	if s := ToolCalled("nonexistent").Score(res); s.Pass {
		t.Fatalf("uncalled tool should not pass: %+v", s)
	}
	if !strings.Contains(ToolCalled("lookup").Score(res).Detail, "1 time") {
		t.Fatalf("detail should report count: %q", ToolCalled("lookup").Score(res).Detail)
	}
}

func TestMaxStepsAndStepCount(t *testing.T) {
	src := lookupSource(t)
	res := runCase(t, src, Case{Name: "steps", Input: "get x"},
		toolCallTurn("c1", "lookup", `{"key":"x"}`),
		agent.StubTurn{Text: "done"},
	) // two steps: one tool call, one text
	if s := MaxSteps(2).Score(res); !s.Pass {
		t.Fatalf("MaxSteps(2) should pass at 2 steps: %+v", s)
	}
	if s := MaxSteps(1).Score(res); s.Pass {
		t.Fatalf("MaxSteps(1) should fail at 2 steps: %+v", s)
	}
	if s := StepCount(2).Score(res); !s.Pass {
		t.Fatalf("StepCount(2) should pass: %+v", s)
	}
	if s := StepCount(3).Score(res); s.Pass {
		t.Fatalf("StepCount(3) should fail: %+v", s)
	}
}

func TestNoErrorCleanRun(t *testing.T) {
	src := lookupSource(t)
	res := runCase(t, src, Case{Name: "clean", Input: "get x"},
		toolCallTurn("c1", "lookup", `{"key":"x"}`),
		agent.StubTurn{Text: "ok"},
	)
	if s := NoError().Score(res); !s.Pass {
		t.Fatalf("clean run should pass NoError: %+v", s)
	}
}

func TestNoErrorCatchesToolErrorResult(t *testing.T) {
	src := lookupSource(t)
	// "boom" runs and returns an IsError result -> tool-end(IsError), not a
	// dispatch error; NoError must still fail.
	res := runCase(t, src, Case{Name: "boom", Input: "explode"},
		toolCallTurn("c1", "boom", `{}`),
		agent.StubTurn{Text: "noted"},
	)
	s := NoError().Score(res)
	if s.Pass {
		t.Fatalf("NoError should catch an IsError tool result: %+v", s)
	}
	if !strings.Contains(s.Detail, "boom") {
		t.Fatalf("detail should name the failing tool: %q", s.Detail)
	}
}

func TestNoErrorCatchesDispatchError(t *testing.T) {
	// Unknown tool -> tool-error dispatch event; loop continues but NoError fails.
	res := runCase(t, agent.NewFuncSource(), Case{Name: "unknown", Input: "x"},
		toolCallTurn("c1", "ghost", `{}`),
		agent.StubTurn{Text: "recovered"},
	)
	if s := NoError().Score(res); s.Pass {
		t.Fatalf("NoError should catch a dispatch error: %+v", s)
	}
}

func TestSuiteAggregate(t *testing.T) {
	src := lookupSource(t)
	// Two cases x two scorers. Case A passes both; case B fails ExactMatch.
	suite := Suite{
		Config: agent.RunnerConfig{
			Provider: agent.NewStubProvider(
				// case A: one tool call then the exact answer
				toolCallTurn("c1", "lookup", `{"key":"x"}`),
				agent.StubTurn{Text: "value-for-x"},
				// case B: a plain wrong answer
				agent.StubTurn{Text: "wrong"},
			),
			Tools: src,
		},
		Cases: []Case{
			{Name: "A-uses-tool", Input: "get x"},
			{Name: "B-wrong-answer", Input: "guess"},
		},
		Scorers: []Scorer{
			ExactMatch("value-for-x"),
			NoError(),
		},
	}

	report := suite.Run(context.Background())
	if report.Total != 2 || report.Passed != 1 || report.Failed != 1 {
		t.Fatalf("aggregate = %+v", report)
	}
	if report.Pass() {
		t.Fatal("suite with a failing case should not Pass()")
	}

	byName := map[string]CaseReport{}
	for _, c := range report.Cases {
		byName[c.Case] = c
	}
	if !byName["A-uses-tool"].Pass {
		t.Fatalf("case A should pass: %+v", byName["A-uses-tool"])
	}
	if byName["B-wrong-answer"].Pass {
		t.Fatalf("case B should fail: %+v", byName["B-wrong-answer"])
	}
	// Each case carries one score per scorer.
	if len(byName["A-uses-tool"].Scores) != 2 {
		t.Fatalf("expected 2 scores per case, got %+v", byName["A-uses-tool"].Scores)
	}

	// Report renders without printing and mentions both cases.
	rendered := report.String()
	if !strings.Contains(rendered, "A-uses-tool") || !strings.Contains(rendered, "1/2 cases passed") {
		t.Fatalf("report render:\n%s", rendered)
	}
}

func TestSuiteRecordsHarnessErrorPerCase(t *testing.T) {
	// No provider -> every case fails to build; the suite records RunErr and
	// keeps going rather than aborting.
	suite := Suite{
		Config:  agent.RunnerConfig{},
		Cases:   []Case{{Name: "a"}, {Name: "b"}},
		Scorers: []Scorer{NoError()},
	}
	report := suite.Run(context.Background())
	if report.Failed != 2 || report.Passed != 0 {
		t.Fatalf("aggregate = %+v", report)
	}
	for _, c := range report.Cases {
		if c.RunErr == "" || c.Pass {
			t.Fatalf("case %q should carry a run error and fail: %+v", c.Case, c)
		}
	}
}

func TestSuiteReportJSONRoundTrips(t *testing.T) {
	report := SuiteReport{
		Total: 1, Passed: 1,
		Cases: []CaseReport{{
			Case: "x", Pass: true,
			Scores: []Score{{Name: "ExactMatch", Pass: true, Value: 1.0, Detail: "ok"}},
		}},
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var back SuiteReport
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Passed != 1 || len(back.Cases) != 1 || back.Cases[0].Scores[0].Name != "ExactMatch" {
		t.Fatalf("round-trip drift: %+v", back)
	}
}
