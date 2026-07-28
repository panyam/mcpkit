package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/panyam/mcpkit/core"
)

// SignalKind classifies an upward signal a child agent raises to its parent
// Runner — the control-axis complement to the downward Control (issue 936).
type SignalKind string

const (
	// SignalEscalate asks the parent to stop and handle the child's finding
	// ("decisive result" / "an error you must deal with"). With the
	// AbortOnEscalate policy the parent turn ends; otherwise the signal is
	// injected and the parent model decides.
	SignalEscalate SignalKind = "escalate"

	// SignalCancelSiblings asks the parent to cancel the child's in-flight
	// siblings ("I found it; the others are wasted work"). In the
	// non-interruptible turn (issue 1165) this intent is RECORDED and surfaced,
	// but the siblings have already joined by the time the parent reads it at the
	// dispatch barrier; acting on it mid-flight is the interruptible turn (issue
	// 1167). Do not mistake it for working cancellation yet.
	SignalCancelSiblings SignalKind = "cancel_siblings"

	// SignalCustom carries an application-defined signal (named by Name, with an
	// optional Data payload) for a SignalPolicy or the parent model to interpret.
	SignalCustom SignalKind = "custom"
)

// Signal is an upward message from a child agent to its parent Runner. It is
// wire-serializable (constraint A2) so a remote child raises it identically to
// a co-located one: RaiseSignal writes it into the ctx-threaded sink the parent
// installs around the dispatch that spawned the child, and the parent reads the
// drained signals at the join.
type Signal struct {
	// Kind classifies the signal; a SignalPolicy switches on it.
	Kind SignalKind `json:"kind"`
	// Name names a SignalCustom signal (ignored for the built-in kinds).
	Name string `json:"name,omitempty"`
	// Note is a short reason, surfaced when the signal is injected into the
	// parent's next step.
	Note string `json:"note,omitempty"`
	// Data is an optional JSON payload for a SignalCustom signal (A5: RawJSON).
	Data core.RawJSON `json:"data,omitempty"`
	// Source is the raising child's scope path (agentScope), stamped by
	// RaiseSignal so a policy or the model knows who signalled. Callers do not
	// set it.
	Source string `json:"source,omitempty"`
}

// SignalAction is a SignalPolicy's verdict over the signals drained at one
// dispatch join. The zero value continues the turn (inject-and-proceed).
type SignalAction struct {
	// AbortTurn ends the parent turn immediately with ErrSignalAbort; Reason is
	// folded into the error. Use it for an escalation that should stop the parent.
	AbortTurn bool
	// Reason is the abort reason (and the turn error message). Ignored unless
	// AbortTurn is set.
	Reason string
}

// SignalPolicy decides how a parent Runner reacts to the signals its children
// raised during one dispatch, read at the join. It runs after the fan-out has
// joined (issue 1165 is non-interruptible), so it chooses only whether to abort
// the parent turn; the signals are injected into the next step regardless. Nil
// means inject-and-continue. A policy must be a pure function of the drained
// signals.
type SignalPolicy func(signals []Signal) SignalAction

// AbortOnEscalate is a built-in SignalPolicy that aborts the parent turn as
// soon as a child raises SignalEscalate, folding that child's note into the
// error. Other signal kinds inject-and-continue. It is the deterministic
// counterpart to letting the parent model decide via injection.
func AbortOnEscalate(signals []Signal) SignalAction {
	for _, s := range signals {
		if s.Kind == SignalEscalate {
			reason := s.Note
			if reason == "" {
				reason = fmt.Sprintf("escalated by %s", s.Source)
			}
			return SignalAction{AbortTurn: true, Reason: reason}
		}
	}
	return SignalAction{}
}

// ErrSignalAbort is returned (wrapped) by a turn a SignalPolicy aborted via
// SignalAction.AbortTurn — a child escalated and the parent chose to stop.
// Check with errors.Is; the wrapped message carries the policy's Reason.
var ErrSignalAbort = errors.New("agent: turn aborted by child signal")

// signalSink collects the signals raised by the sub-agents one dispatch
// spawned. Two ctx keys keep the direction right (the subtle part): a dispatch
// installs its sink under dispatchSinkKey (where it collects its OWN children's
// signals), and when an AgentSource spawns a child it snapshots that sink under
// parentSinkKey in the child's ctx (where the child's signal_parent writes).
// RaiseSignal reads parentSinkKey. So a child raises to its SPAWNER's sink, not
// to its own dispatch sink — which the child's own dispatch would otherwise
// shadow — and a grandchild raises to the child, never skipping a level.
type signalSink struct {
	mu      sync.Mutex
	signals []Signal
}

func (s *signalSink) raise(sig Signal) {
	s.mu.Lock()
	s.signals = append(s.signals, sig)
	s.mu.Unlock()
}

// drain returns the collected signals and clears the sink.
func (s *signalSink) drain() []Signal {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.signals
	s.signals = nil
	return out
}

type dispatchSinkKey struct{}
type parentSinkKey struct{}

// withDispatchSink stamps the sink a dispatch collects its children's signals
// into. AgentSource reads it (via dispatchSinkFrom) to hand the spawned child
// its parent sink.
func withDispatchSink(ctx context.Context, s *signalSink) context.Context {
	return context.WithValue(ctx, dispatchSinkKey{}, s)
}

func dispatchSinkFrom(ctx context.Context) *signalSink {
	s, _ := ctx.Value(dispatchSinkKey{}).(*signalSink)
	return s
}

// withParentSink stamps the sink a running child raises TO — snapshotted by the
// spawning AgentSource from the spawner's dispatch sink, so the child's own
// dispatch cannot shadow it.
func withParentSink(ctx context.Context, s *signalSink) context.Context {
	return context.WithValue(ctx, parentSinkKey{}, s)
}

func parentSinkFrom(ctx context.Context) *signalSink {
	s, _ := ctx.Value(parentSinkKey{}).(*signalSink)
	return s
}

// RaiseSignal delivers sig to the parent Runner's signal sink — the
// control-axis "up" primitive a child's control tool calls. It stamps
// sig.Source with the child's scope when unset. It returns false when ctx
// carries no parent sink (a top-level agent has no parent to signal), so a
// control tool can tell the model there is nothing to signal instead of
// silently dropping it.
func RaiseSignal(ctx context.Context, sig Signal) bool {
	sink := parentSinkFrom(ctx)
	if sink == nil {
		return false
	}
	if sig.Source == "" {
		sig.Source = agentScope(ctx)
	}
	sink.raise(sig)
	return true
}

// SignalParentToolName is the fixed name of the control tool a child agent calls
// to raise a Signal to its parent (kept fixed, like the memory/meta-tool names).
const SignalParentToolName = "signal_parent"

type signalParentArgs struct {
	// Kind is "escalate", "cancel_siblings", or "custom".
	Kind string `json:"kind"`
	// Note is a short reason surfaced to the parent.
	Note string `json:"note,omitempty"`
	// Name names a custom signal (kind="custom").
	Name string `json:"name,omitempty"`
}

// NewSignalSource returns a leaf ToolSource exposing the signal_parent control
// tool so a child agent can raise an upward Signal (issue 1165). Add it to a
// sub-agent's tool set; a top-level agent that has no parent gets a graceful
// "no parent to signal" result. Model-facing, so it lives in agent/ (A6).
func NewSignalSource() *FuncSource {
	fs := NewFuncSource()
	_ = AddFunc(fs, SignalParentToolName,
		"Raise a signal to the parent agent that spawned you. kind: 'escalate' (ask the parent to stop and handle your finding), 'cancel_siblings' (you found the answer; the parent's other in-flight sub-agents are wasted work), or 'custom' (a named signal). Use sparingly — only for a decisive result the parent must act on.",
		func(ctx context.Context, in signalParentArgs) (string, error) {
			kind := SignalKind(in.Kind)
			switch kind {
			case SignalEscalate, SignalCancelSiblings, SignalCustom:
			default:
				return "", fmt.Errorf("unknown signal kind %q (want escalate, cancel_siblings, or custom)", in.Kind)
			}
			if RaiseSignal(ctx, Signal{Kind: kind, Name: in.Name, Note: in.Note}) {
				return fmt.Sprintf("signalled parent: %s", kind), nil
			}
			return "no parent to signal (you are the top-level agent)", nil
		})
	return fs
}

// renderSignals formats the drained signals into the RoleSystem note injected
// into the parent's next step, so the parent model sees what its children
// raised. One line per signal.
func renderSignals(signals []Signal) string {
	var b strings.Builder
	b.WriteString("Sub-agent signals received:")
	for _, s := range signals {
		b.WriteString("\n- ")
		b.WriteString(string(s.Kind))
		if s.Name != "" {
			b.WriteString(" (")
			b.WriteString(s.Name)
			b.WriteString(")")
		}
		if s.Source != "" {
			b.WriteString(" from ")
			b.WriteString(s.Source)
		}
		if s.Note != "" {
			b.WriteString(": ")
			b.WriteString(s.Note)
		}
	}
	return b.String()
}
