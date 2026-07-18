package eval

import (
	"context"
	"fmt"

	"github.com/panyam/mcpkit/agent"
)

// Scenario is a multi-turn eval input: a sequence of user turns that share
// one conversation and (optionally) one working-memory scratchpad, run in
// order. It is what the single-turn Case cannot express — memory evals
// (LongMemEval and kin) build up facts across turns and sessions, then ask
// a question, so the thing under test is whether earlier turns survive into
// a later answer.
//
// RunScenario threads history across turns and keeps one MemorySource for
// the whole run, so a fact remembered on turn 1 is recallable on turn 5.
// The last turn is the one a Scorer grades; earlier turns are setup.
type Scenario struct {
	// Name identifies the scenario in a report. Should be unique in a Suite.
	Name string

	// Turns are the user inputs, oldest first. Each runs as one agent turn
	// against the accumulated history; the final turn's Result is the graded
	// one.
	Turns []string

	// Memory, when true, gives the run a working-memory scratchpad (a
	// MemorySource) merged with any base Tools, so the model can
	// remember/recall across the turns. The backing store is NewMemoryStore
	// when set, else a fresh in-memory default. The store is built once per
	// RunScenario call — scenarios never share memory.
	Memory bool

	// NewMemoryStore builds the MemoryStore backing this scenario's working
	// memory. Nil uses the in-memory default. Set it to grade a different
	// backend (redis, gorm, a future semantic store) through the same
	// scenarios — the whole point of an eval harness is comparing impls. It
	// is a factory, not a value, so each RunScenario gets its own store and
	// scenarios in a suite never cross-contaminate; a caller that WANTS
	// shared state across scenarios can close over one store and return it.
	// A non-nil NewMemoryStore implies Memory.
	NewMemoryStore func() (agent.MemoryStore, error)

	// Tools is the base tool surface, merged under the memory tools when
	// Memory is set. Nil offers only the memory tools (or none, when Memory
	// is false).
	Tools agent.ToolSource

	// Instructions overrides RunnerConfig.Instructions for this scenario
	// when non-empty.
	Instructions string

	// MaxSteps overrides RunnerConfig.MaxSteps per turn when positive.
	MaxSteps int
}

// RunScenario executes s against a copy of cfg with the scenario's overrides
// applied, running each turn in order and threading history plus a shared
// working-memory source across them. It returns one Result per turn (the
// last is the graded turn); the returned error is a harness-level failure
// (an invalid RunnerConfig), while a turn that runs and fails carries its
// error in that turn's Result.Err.
//
// A single Runner is built and reused for every turn: the Runner is
// stateless over the history it is handed, so reuse is what makes the shared
// MemorySource (held in cfg.Tools) persist across turns.
func RunScenario(ctx context.Context, cfg agent.RunnerConfig, s Scenario) ([]Result, error) {
	if s.Instructions != "" {
		cfg.Instructions = s.Instructions
	}
	if s.MaxSteps > 0 {
		cfg.MaxSteps = s.MaxSteps
	}
	if s.Tools != nil {
		cfg.Tools = s.Tools
	}
	if s.Memory || s.NewMemoryStore != nil {
		store := agent.MemoryStore(agent.NewInMemoryMemoryStore())
		if s.NewMemoryStore != nil {
			var err error
			if store, err = s.NewMemoryStore(); err != nil {
				return nil, fmt.Errorf("eval: scenario %q memory store: %w", s.Name, err)
			}
		}
		mem, err := agent.NewMemorySource(store)
		if err != nil {
			return nil, fmt.Errorf("eval: scenario %q memory: %w", s.Name, err)
		}
		if cfg.Tools == nil {
			cfg.Tools = mem
		} else {
			multi := agent.NewMultiSource()
			if err := multi.Add("base", cfg.Tools); err != nil {
				return nil, fmt.Errorf("eval: scenario %q tools: %w", s.Name, err)
			}
			if err := multi.Add("memory", mem); err != nil {
				return nil, fmt.Errorf("eval: scenario %q memory: %w", s.Name, err)
			}
			cfg.Tools = multi
		}
	}

	runner, err := agent.NewRunner(cfg)
	if err != nil {
		return nil, fmt.Errorf("eval: build runner for scenario %q: %w", s.Name, err)
	}

	var history []agent.Message
	results := make([]Result, 0, len(s.Turns))
	for i, input := range s.Turns {
		history = append(history, agent.Message{Role: agent.RoleUser, Text: input})

		var events []agent.Event
		emit := func(e agent.Event) { events = append(events, e) }

		turn, runErr := runner.Run(ctx, history, emit)
		// Each turn is named "<scenario>#<n>" so a per-turn Result is
		// traceable back to its position in the scenario.
		c := Case{Name: fmt.Sprintf("%s#%d", s.Name, i+1), Input: input}
		results = append(results, Result{Case: c, Turn: turn, Events: events, Err: runErr})
		if turn != nil {
			history = append(history, turn.Messages...)
		}
	}
	return results, nil
}

// Final returns the last turn's Result — the graded turn of a scenario. It
// panics on an empty slice (a scenario always has at least one turn), so a
// scorer can write Final(results) without a length dance.
func Final(results []Result) Result {
	return results[len(results)-1]
}
