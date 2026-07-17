package eval

import (
	"context"
	"fmt"
	"strings"

	"github.com/panyam/mcpkit/agent"
)

// Suite runs each Case against every Scorer and folds the verdicts into a
// SuiteReport. Config is the base RunnerConfig; each Case may override its
// tool surface, instructions, or step cap on top of it.
type Suite struct {
	// Config is the base RunnerConfig shared by every case (Provider is
	// required). Per-case overrides in Case are layered on a copy.
	Config agent.RunnerConfig

	// Cases is the set of inputs to evaluate.
	Cases []Case

	// Scorers is the set of graders applied to every case's Result.
	Scorers []Scorer
}

// CaseReport is one case's outcome: the per-scorer verdicts and whether the
// case passed overall (every scorer passed and the run built cleanly).
type CaseReport struct {
	Case   string  `json:"case"`
	Scores []Score `json:"scores"`
	Pass   bool    `json:"pass"`
	// RunErr is set when the harness could not build the runner for this
	// case (an invalid config); when set, no scorers ran and Pass is false.
	RunErr string `json:"runErr,omitempty"`
}

// SuiteReport is the aggregate: one CaseReport per case plus pass/fail counts.
type SuiteReport struct {
	Cases  []CaseReport `json:"cases"`
	Passed int          `json:"passed"`
	Failed int          `json:"failed"`
	Total  int          `json:"total"`
}

// Pass reports whether every case passed.
func (r SuiteReport) Pass() bool { return r.Failed == 0 }

// Run evaluates every case against every scorer and returns the report. It
// does not stop on a failing case; a per-case harness failure (bad config) is
// recorded in that case's RunErr and the suite continues. Ctx cancellation
// surfaces per case via the Runner (recorded in scores through NoError, or in
// RunErr for a build failure) rather than aborting the whole suite.
func (s Suite) Run(ctx context.Context) SuiteReport {
	report := SuiteReport{Total: len(s.Cases)}
	for _, c := range s.Cases {
		cr := CaseReport{Case: c.Name, Pass: true}

		res, err := Run(ctx, s.Config, c)
		if err != nil {
			cr.RunErr = err.Error()
			cr.Pass = false
			report.Cases = append(report.Cases, cr)
			report.Failed++
			continue
		}

		for _, sc := range s.Scorers {
			verdict := sc.Score(res)
			cr.Scores = append(cr.Scores, verdict)
			if !verdict.Pass {
				cr.Pass = false
			}
		}
		if len(s.Scorers) == 0 {
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
