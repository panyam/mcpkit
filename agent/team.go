package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/panyam/mcpkit/core"
)

// DefaultMaxHandoffs bounds how many times a Team may transfer control before
// giving up — the ping-pong backstop (two agents that keep handing the
// conversation back and forth).
const DefaultMaxHandoffs = 8

// ErrMaxHandoffs is returned by Team.Run when the handoff cap is exceeded.
var ErrMaxHandoffs = errors.New("agent: max handoffs exceeded")

// Team runs a set of named agents that transfer control to one another —
// handoff, the one composition mode that does NOT fit agent-as-tool
// (AgentSource). Where a sub-agent is called and returns an answer, a handoff
// transfers the whole conversation to a specialist and does not come back: the
// active agent runs, and if it calls a transfer_to_<name> tool, the Team swaps
// the active agent and continues over the SAME (carried-forward) history. The
// Runner is unaware it was swapped — the transfer is a tool it happened to
// call, intercepted above it.
//
// A6: model-facing (agents + transfer tools), so it lives in agent/.
type Team struct {
	members     map[string]*teamMember
	order       []string
	start       string
	maxHandoffs int
	onHandoff   func(from, to string)
}

type teamMember struct {
	name   string
	runner *Runner
}

// TeamMember declares one agent in a Team and which other agents it may hand
// off to.
type TeamMember struct {
	// Name identifies the agent and forms its transfer tool (transfer_to_<Name>
	// on any teammate allowed to reach it). Required, unique within the Team.
	Name string

	// Config is the agent's RunnerConfig (provider, instructions, its own
	// tools). The Team merges the transfer tools in; the agent's own Tools are
	// preserved. Required.
	Config RunnerConfig

	// HandoffTo lists the names this agent may transfer to. Each becomes a
	// transfer_to_<name> tool offered only to this agent. Empty means a
	// terminal agent that must answer rather than hand off. Every name must be
	// another member.
	HandoffTo []string
}

// TeamConfig assembles a Team.
type TeamConfig struct {
	// Members are the agents. At least one; Start must name one of them.
	Members []TeamMember

	// Start is the agent that receives the first turn. Required.
	Start string

	// MaxHandoffs caps transfers before Run gives up with ErrMaxHandoffs.
	// Zero uses DefaultMaxHandoffs.
	MaxHandoffs int

	// OnHandoff, when set, is called on each transfer (from, to) — the seam a
	// surface renders "→ handed off to X" through.
	OnHandoff func(from, to string)
}

// NewTeam validates cfg and builds each agent's Runner with its transfer tools
// merged in. It errors on a missing/duplicate member name, an unknown Start,
// or a HandoffTo naming a non-member.
func NewTeam(cfg TeamConfig) (*Team, error) {
	if len(cfg.Members) == 0 {
		return nil, fmt.Errorf("agent: Team needs at least one member")
	}
	names := make(map[string]bool, len(cfg.Members))
	for _, m := range cfg.Members {
		if m.Name == "" {
			return nil, fmt.Errorf("agent: Team member with empty Name")
		}
		if names[m.Name] {
			return nil, fmt.Errorf("agent: Team has duplicate member %q", m.Name)
		}
		names[m.Name] = true
	}
	if !names[cfg.Start] {
		return nil, fmt.Errorf("agent: Team Start %q is not a member", cfg.Start)
	}

	t := &Team{
		members:     make(map[string]*teamMember, len(cfg.Members)),
		start:       cfg.Start,
		maxHandoffs: cfg.MaxHandoffs,
		onHandoff:   cfg.OnHandoff,
	}
	if t.maxHandoffs <= 0 {
		t.maxHandoffs = DefaultMaxHandoffs
	}

	for _, m := range cfg.Members {
		mc := m.Config
		if len(m.HandoffTo) > 0 {
			transfers, err := buildTransferSource(m.Name, m.HandoffTo, names)
			if err != nil {
				return nil, err
			}
			if mc.Tools == nil {
				mc.Tools = transfers
			} else {
				multi := NewMultiSource()
				if err := multi.Add("base", mc.Tools); err != nil {
					return nil, fmt.Errorf("agent: Team %q tools: %w", m.Name, err)
				}
				if err := multi.Add("handoff", transfers); err != nil {
					return nil, fmt.Errorf("agent: Team %q handoff tools: %w", m.Name, err)
				}
				mc.Tools = multi
			}
		}
		runner, err := NewRunner(mc)
		if err != nil {
			return nil, fmt.Errorf("agent: Team member %q: %w", m.Name, err)
		}
		t.members[m.Name] = &teamMember{name: m.Name, runner: runner}
		t.order = append(t.order, m.Name)
	}
	return t, nil
}

// buildTransferSource makes the transfer_to_<target> tools for one agent. Each
// records the requested target on the ctx handoff signal and acks; the Team
// reads the signal after the agent's turn.
func buildTransferSource(from string, targets []string, members map[string]bool) (*FuncSource, error) {
	fs := NewFuncSource()
	for _, target := range targets {
		if !members[target] {
			return nil, fmt.Errorf("agent: Team member %q hands off to unknown member %q", from, target)
		}
		target := target
		if err := AddFunc(fs, "transfer_to_"+target,
			"Hand off the conversation to the "+target+" agent. Call this to transfer control; do not also answer.",
			func(ctx context.Context, _ struct{}) (string, error) {
				if sig := handoffSignalFrom(ctx); sig != nil {
					sig.set(target)
				}
				return "Transferring to " + target + ".", nil
			}); err != nil {
			return nil, err
		}
	}
	return fs, nil
}

// Run drives the conversation: the Start agent runs over the input, and each
// time an agent transfers, the Team swaps the active agent and continues over
// the carried-forward history until an agent answers without transferring (the
// final result) or the handoff cap is hit (ErrMaxHandoffs). All agents share
// one history (handoff transfers context); each brings its own instructions.
// emit receives every agent's events; OnHandoff marks the transitions.
func (t *Team) Run(ctx context.Context, input string, emit func(Event)) (*TurnResult, error) {
	if emit == nil {
		emit = func(Event) {}
	}
	sig := &handoffSignal{}
	ctx = withHandoffSignal(ctx, sig)

	history := []Message{{Role: RoleUser, Text: input}}
	active := t.members[t.start]
	handoffs := 0
	for {
		result, err := active.runner.Run(ctx, history, emit)
		if err != nil {
			return nil, err
		}
		history = append(history, result.Messages...)

		target := sig.take()
		if target == "" {
			return result, nil
		}
		next, ok := t.members[target]
		if !ok {
			// The transfer tools only offer real members, so this is
			// unreachable; treat a stray target as "no transfer".
			return result, nil
		}
		if handoffs >= t.maxHandoffs {
			return nil, fmt.Errorf("%w (%d) starting from %q", ErrMaxHandoffs, t.maxHandoffs, t.start)
		}
		handoffs++
		if t.onHandoff != nil {
			t.onHandoff(active.name, target)
		}
		active = next
	}
}

// ToolDefs exposes the merged tool set each member offers, for inspection
// (which agents can hand off where). Keyed by member name.
func (t *Team) ToolDefs(ctx context.Context) (map[string][]core.ToolDef, error) {
	out := make(map[string][]core.ToolDef, len(t.members))
	for name, m := range t.members {
		if m.runner.cfg.Tools == nil {
			continue
		}
		defs, err := m.runner.cfg.Tools.Tools(ctx)
		if err != nil {
			return nil, err
		}
		out[name] = defs
	}
	return out, nil
}

type handoffKey struct{}

// handoffSignal is the per-Run channel between a transfer tool and the Team
// loop, threaded on ctx so concurrent Team.Run calls never share it.
type handoffSignal struct {
	mu     sync.Mutex
	target string
}

func (h *handoffSignal) set(t string) {
	h.mu.Lock()
	h.target = t
	h.mu.Unlock()
}

// take reads and clears the pending target, so each round starts fresh.
func (h *handoffSignal) take() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	t := h.target
	h.target = ""
	return t
}

func withHandoffSignal(ctx context.Context, sig *handoffSignal) context.Context {
	return context.WithValue(ctx, handoffKey{}, sig)
}

func handoffSignalFrom(ctx context.Context) *handoffSignal {
	sig, _ := ctx.Value(handoffKey{}).(*handoffSignal)
	return sig
}
