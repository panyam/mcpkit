package eval

import (
	"context"
	"fmt"
	"strings"

	"github.com/panyam/mcpkit/experimental/agent"
)

// SuiteCase is one graded unit: a scenario to run and the scorers that grade
// it.
//
// Scorers are per-case rather than per-suite because that is what an external
// benchmark is. Every question in a memory benchmark has its own expected
// answer, so a single scorer list shared across the suite can only express
// properties that hold for every case ("did not error", "called a tool"),
// never the ground truth that makes a benchmark a benchmark. The previous
// shape had exactly that limitation, and longmemeval worked around it by
// hand-rolling its own runner loop.
type SuiteCase struct {
	// Scenario is the conversation to run. Its Name identifies the case in
	// the report.
	Scenario Scenario

	// Scorers builds this case's graders. It takes the provider because a
	// rubric scorer (Judge) needs a model to grade with, and the suite owns
	// the provider; a case that only uses deterministic scorers ignores the
	// argument.
	//
	// A case with no scorers fails, on the same reasoning as before: a case
	// nothing grades is a case that silently passes forever.
	Scorers func(agent.Provider) []Scorer

	// Configure adapts the suite's base RunnerConfig for this case. Nil uses
	// the base unchanged.
	//
	// It exists because some cases are about the harness rather than the
	// model: a compaction case has to run under a Compactor, which needs the
	// provider to build. Returning a config rather than mutating one keeps
	// each case's changes from leaking into the next.
	Configure func(agent.RunnerConfig) (agent.RunnerConfig, error)
}

// Single wraps a single-turn Case with a fixed scorer list, for the
// table-driven style that does not need per-case ground truth.
//
// It exists so the common shape stays one line after Suite moved to
// scenarios. There is one graded unit, not two: this builds the same
// SuiteCase an adapter yields.
func Single(c Case, scorers ...Scorer) SuiteCase {
	return SuiteCase{
		Scenario: Scenario{
			Name:         c.Name,
			History:      c.History,
			Turns:        []string{c.Input},
			Tools:        c.Tools,
			Instructions: c.Instructions,
			MaxSteps:     c.MaxSteps,
		},
		Scorers: func(agent.Provider) []Scorer { return scorers },
	}
}

// Suite is a set of graded cases sharing a base config.
type Suite struct {
	// Config is the base RunnerConfig shared by every case (Provider is
	// required). Each case's Configure is layered on a copy.
	Config agent.RunnerConfig

	// Cases is the set of graded units to evaluate.
	Cases []SuiteCase
}

// CaseReport is one case's outcome: the per-scorer verdicts and whether the
// case passed overall (every scorer passed and the run built cleanly).
type CaseReport struct {
	Case   string  `json:"case"`
	Scores []Score `json:"scores"`
	Pass   bool    `json:"pass"`
	// RunErr is set when the harness could not run this case (an invalid
	// config, a case with no turns); when set, no scorers ran and Pass is
	// false.
	RunErr string `json:"runErr,omitempty"`
}

// SuiteReport is the aggregate: one CaseReport per case plus pass/fail counts.
//
// Passed and Failed count whole cases, where a case passes only if every one
// of its scorers passed. That is the right aggregate for a benchmark asking
// one question per case, and the wrong one for a benchmark reporting several
// independent rates — a security suite wants utility and attack-success
// counted separately, and one of them inverted. Per-scorer verdicts are kept
// on each CaseReport with their names, so a per-dimension rollup is a pure
// addition over data already here rather than a re-plumb. See issue 1060.
type SuiteReport struct {
	Cases  []CaseReport `json:"cases"`
	Passed int          `json:"passed"`
	Failed int          `json:"failed"`
	Total  int          `json:"total"`
}

// Pass reports whether every case passed.
func (r SuiteReport) Pass() bool { return r.Failed == 0 }

// Run evaluates every case against its own scorers and returns the report. It
// does not stop on a failing case; a per-case harness failure is recorded in
// that case's RunErr and the suite continues. Ctx cancellation surfaces per
// case via the Runner (recorded in scores through NoError, or in RunErr for a
// build failure) rather than aborting the whole suite.
func (s Suite) Run(ctx context.Context) SuiteReport {
	report := SuiteReport{Total: len(s.Cases)}
	for _, sc := range s.Cases {
		cr := CaseReport{Case: sc.Scenario.Name, Pass: true}

		fail := func(format string, args ...any) {
			cr.RunErr = fmt.Sprintf(format, args...)
			cr.Pass = false
			report.Cases = append(report.Cases, cr)
			report.Failed++
		}

		cfg := s.Config
		if sc.Configure != nil {
			adapted, err := sc.Configure(cfg)
			if err != nil {
				fail("configure: %v", err)
				continue
			}
			cfg = adapted
		}

		results, err := RunScenario(ctx, cfg, sc.Scenario)
		if err != nil {
			fail("%v", err)
			continue
		}
		// Final panics on an empty slice, and a case whose Scenario has no
		// Turns produces one. That is a plausible adapter bug (History
		// populated, the question forgotten), so it is reported as this
		// case's failure rather than taking the suite down.
		if len(results) == 0 {
			fail("scenario %q ran no turns", sc.Scenario.Name)
			continue
		}
		final := Final(results)

		var scorers []Scorer
		if sc.Scorers != nil {
			scorers = sc.Scorers(s.Config.Provider)
		}
		for _, scorer := range scorers {
			verdict := scorer.Score(final)
			cr.Scores = append(cr.Scores, verdict)
			if !verdict.Pass {
				cr.Pass = false
			}
		}
		if len(scorers) == 0 {
			cr.Pass = false
		}

		report.Cases = append(report.Cases, cr)
		if cr.Pass {
			report.Passed++
		} else {
			report.Failed++
		}
	}
	return report
}

// String renders the report as a plain-text table. It returns the string; it
// does not print (constraint A4) — a test or CLI writes it. The format is
// stable enough to eyeball, not a machine contract (marshal the report as JSON
// for that).
func (r SuiteReport) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "eval suite: %d/%d cases passed\n", r.Passed, r.Total)
	for _, c := range r.Cases {
		status := "PASS"
		if !c.Pass {
			status = "FAIL"
		}
		fmt.Fprintf(&b, "  [%s] %s\n", status, c.Case)
		if c.RunErr != "" {
			fmt.Fprintf(&b, "      run error: %s\n", c.RunErr)
			continue
		}
		for _, sc := range c.Scores {
			mark := "ok"
			if !sc.Pass {
				mark = "x "
			}
			fmt.Fprintf(&b, "      %s %-12s %.2f  %s\n", mark, sc.Name, sc.Value, sc.Detail)
		}
	}
	return b.String()
}

// DimensionReport is one scorer name's outcome across a whole suite.
type DimensionReport struct {
	// Name is the scorer name these counts aggregate.
	Name string `json:"name"`

	// Passed and Failed count the cases carrying a verdict under this name.
	// Total is their sum, and is not necessarily the suite's case count: a
	// dimension only some cases are graded on has a smaller Total.
	Passed int `json:"passed"`
	Failed int `json:"failed"`
	Total  int `json:"total"`
}

// Rate is the fraction of graded cases that passed this dimension, 0 when
// nothing was graded on it.
func (d DimensionReport) Rate() float64 {
	if d.Total == 0 {
		return 0
	}
	return float64(d.Passed) / float64(d.Total)
}

// Dimensions aggregates verdicts by scorer name, in first-seen order.
//
// Passed/Failed on SuiteReport count whole cases, where a case passes only if
// every one of its scorers passed. That is the right aggregate for a benchmark
// asking one question per case, and the wrong one for a suite reporting
// several independent rates: a security suite measures whether the agent did
// the user's job AND whether it resisted an injected instruction, and those
// move independently. Collapsing them loses the quadrant that matters most,
// the run that succeeded at the task while also obeying the attacker.
//
// This is deliberately a method over data CaseReport already carries rather
// than new fields, so a suite that does not need it is unaffected and nothing
// had to be re-plumbed to add it. Whether a dimension reads better inverted
// (an attack-success rate is 1-Rate of a "resisted" dimension) is the caller's
// presentation decision, not something encoded here.
func (r SuiteReport) Dimensions() []DimensionReport {
	var order []string
	byName := map[string]*DimensionReport{}
	for _, c := range r.Cases {
		for _, s := range c.Scores {
			d, ok := byName[s.Name]
			if !ok {
				d = &DimensionReport{Name: s.Name}
				byName[s.Name] = d
				order = append(order, s.Name)
			}
			d.Total++
			if s.Pass {
				d.Passed++
			} else {
				d.Failed++
			}
		}
	}
	out := make([]DimensionReport, 0, len(order))
	for _, name := range order {
		out = append(out, *byName[name])
	}
	return out
}
