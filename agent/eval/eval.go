// Package eval is a deterministic eval / scorer harness for agent turns. A
// Case names one input; a Scorer grades what a Run produced (the TurnResult
// plus the captured event stream); a Suite runs a set of Cases against a set
// of Scorers and returns a structured report.
//
// The harness is model-and-turn-facing, so it lives in agent/ (constraint
// A6): scorers read a *agent.TurnResult and an []agent.Event, both meaningless
// without a Runner turn. It depends only on core/ and agent/, adds no
// third-party dependency, and never prints (constraint A4) — a Suite returns a
// SuiteReport that a test or CLI renders.
//
// The deterministic scorers (ExactMatch, Contains, ToolCalled, MaxSteps,
// StepCount, NoError) need no network or live model: drive a Case with
// agent.NewStubProvider for reproducible evals in CI. The LLM-as-judge scorer
// lives behind the eval_llm build tag so the default build and CI path carry
// no model requirement.
package eval

import (
	"context"
	"fmt"
	"strings"

	"github.com/panyam/mcpkit/agent"
)

// Case is one eval input: a name, the conversation to run, and any per-case
// overrides applied on top of the base RunnerConfig. It is a plain struct so
// callers can build tables of them inline.
type Case struct {
	// Name identifies the case in a report. Should be unique within a Suite.
	Name string

	// Input is a convenience seed: when History is empty and Input is
	// non-empty, the turn runs against a single RoleUser message carrying
	// Input. History takes precedence when both are set.
	Input string

	// History is the full input conversation, oldest first. Overrides Input.
	History []agent.Message

	// Tools overrides RunnerConfig.Tools for this case when non-nil, so a
	// case can run under a tool surface tailored to what it exercises.
	Tools agent.ToolSource

	// Instructions overrides RunnerConfig.Instructions for this case when
	// non-empty.
	Instructions string

	// MaxSteps overrides RunnerConfig.MaxSteps for this case when positive.
	MaxSteps int
}

// history resolves the messages a Case runs against: History verbatim, else a
// single user message from Input, else nil.
func (c Case) history() []agent.Message {
	if len(c.History) > 0 {
		return c.History
	}
	if c.Input != "" {
		return []agent.Message{{Role: agent.RoleUser, Text: c.Input}}
	}
	return nil
}

// Result is one scored run's transcript: the completed turn (nil when the run
// failed before producing one), the captured event stream, and the run error
// (nil on success). Scorers read these fields; NoError inspects Err and the
// event stream, the text scorers read Turn.Text.
type Result struct {
	// Case is the input that produced this result.
	Case Case

	// Turn is the completed turn, or nil when Err is non-nil and the turn
	// aborted before completion.
	Turn *agent.TurnResult

	// Events is every event the Runner emitted, in order.
	Events []agent.Event

	// Err is the run error: ctx cancellation, provider failure, or the step
	// cap (wrapping agent.ErrMaxSteps). Tool failures do not appear here —
	// the Runner feeds those back to the model, so they surface as
	// tool-error / tool-end(IsError) events instead.
	Err error
}

// Run executes one Case against a copy of cfg with the Case's overrides
// applied, wiring an event collector, and packages the transcript into a
// Result. The returned error is a harness-level failure (an invalid
// RunnerConfig that NewRunner rejects); a turn that runs and fails carries its
// error in Result.Err, not the returned error, so scorers can grade it.
func Run(ctx context.Context, cfg agent.RunnerConfig, c Case) (Result, error) {
	if c.Tools != nil {
		cfg.Tools = c.Tools
	}
	if c.Instructions != "" {
		cfg.Instructions = c.Instructions
	}
	if c.MaxSteps > 0 {
		cfg.MaxSteps = c.MaxSteps
	}

	runner, err := agent.NewRunner(cfg)
	if err != nil {
		return Result{Case: c}, fmt.Errorf("eval: build runner for case %q: %w", c.Name, err)
	}

	var events []agent.Event
	emit := func(e agent.Event) { events = append(events, e) }

	turn, runErr := runner.Run(ctx, c.history(), emit)
	return Result{Case: c, Turn: turn, Events: events, Err: runErr}, nil
}

// Transcript renders a Result as human-readable text: the final answer, the
// step count, and the tool calls the turn made with their results. The
// LLM-as-judge scorer feeds this to a model; it is also useful for logging a
// failed case. Rendering only, no printing (constraint A4).
func Transcript(r Result) string {
	var b strings.Builder
	if r.Turn != nil {
		fmt.Fprintf(&b, "steps: %d\n", r.Turn.Steps)
		if r.Turn.FinishReason != "" {
			fmt.Fprintf(&b, "finish: %s\n", r.Turn.FinishReason)
		}
	}
	if r.Err != nil {
		fmt.Fprintf(&b, "run error: %v\n", r.Err)
	}
	for _, e := range r.Events {
		switch e.Kind {
		case agent.EventToolBegin:
			if e.ToolCall != nil {
				fmt.Fprintf(&b, "tool call: %s(%s)\n", e.ToolCall.Name, strings.TrimSpace(string(e.ToolCall.Args.Raw())))
			}
		case agent.EventToolEnd:
			marker := "ok"
			if e.ToolResult != nil && e.ToolResult.IsError {
				marker = "error"
			}
			fmt.Fprintf(&b, "tool result (%s)\n", marker)
		case agent.EventToolError:
			fmt.Fprintf(&b, "tool dispatch error: %s\n", e.Error)
		}
	}
	if r.Turn != nil {
		fmt.Fprintf(&b, "answer: %s\n", r.Turn.Text)
	}
	return b.String()
}
