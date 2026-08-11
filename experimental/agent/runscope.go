package agent

import (
	"context"
	"strings"
)

// AgentHop is one link in a run's agent ancestry: the sub-agent that was
// invoked at that level.
//
// It is a struct rather than a bare name so a hop can grow without breaking
// every caller. A child's location is not guaranteed — the in-process
// AgentSource is the degenerate co-located case, and constraint A7 states the
// general case is a child on another host, provider, or model. When that
// arrives a hop gains where it ran; adding a field here is a minor change,
// while widening []string to []AgentHop later would not have been.
type AgentHop struct {
	// Name is the sub-agent's registered name, as given to AgentSource,
	// FanOutSource, Team, or AgentPool.
	Name string `json:"name"`
}

// AgentPath is a run's agent ancestry, outermost first: who invoked whom to
// reach the code reading it. Empty at the top level.
//
// Read it as a stack trace rather than a graph path. Each entry is a frame,
// which is why a cycle needs no special handling: an agent that reaches itself
// appears twice, exactly as recursion shows repeated frames, and the per-source
// depth cap (DefaultMaxAgentDepth) bounds how far that can go.
type AgentPath []AgentHop

// Depth reports how deep in the agent tree this path is: 0 at the top level.
func (p AgentPath) Depth() int { return len(p) }

// Contains reports whether name appears anywhere in the ancestry, which is the
// exact form of "am I running under X".
//
// The reason this exists rather than callers matching on String: substring
// matching against a joined path silently answers yes for "research" when the
// ancestor is "researcher", and splitting a joined path is ambiguous because a
// name may itself contain the separator.
func (p AgentPath) Contains(name string) bool {
	for _, h := range p {
		if h.Name == name {
			return true
		}
	}
	return false
}

// Child returns the path extended by one hop, without mutating the receiver.
// This is what each source calls when it invokes a sub-agent.
func (p AgentPath) Child(name string) AgentPath {
	out := make(AgentPath, len(p), len(p)+1)
	copy(out, p)
	return append(out, AgentHop{Name: name})
}

// String renders the ancestry slash-joined ("researcher/summarizer") for
// display, tracing, and Signal.Source. Lossy when a name contains a slash, so
// use it to show a path and Contains to test one.
func (p AgentPath) String() string {
	if len(p) == 0 {
		return ""
	}
	names := make([]string, len(p))
	for i, h := range p {
		names[i] = h.Name
	}
	return strings.Join(names, "/")
}

// TreeUsage is how much of a turn's aggregate TreeBudget has been consumed, so
// an extension can back off before exhaustion rather than discovering it as
// ErrTreeBudget, which aborts.
//
// A zero Max means that dimension is unbounded, which is also what an entirely
// unbudgeted turn reports. Usage is only tracked for a dimension that has a
// cap, so StepsUsed and TokensUsed read 0 when their Max does — the counter
// exists to enforce a limit, and there is none to enforce.
//
// Both counts are post-hoc for the same reason TreeBudget.MaxTokens is: usage
// is known only after a model call, so a reading can be one step stale.
type TreeUsage struct {
	StepsUsed  int `json:"stepsUsed"`
	MaxSteps   int `json:"maxSteps"`
	TokensUsed int `json:"tokensUsed"`
	MaxTokens  int `json:"maxTokens"`
}

// RunScope is what a call knows about the run it belongs to, beyond its own
// arguments: where it sits in the agent tree and what the tree has spent.
//
// It exists because the Extension seam and the run's own state pulled against
// each other. A middleware received only its call, so a checkpoint extension
// could not tell it was running inside a sub-agent and snapshotted on every
// nested call, and a budget-aware extension could not read how much tree
// budget remained.
//
// Read-only by construction. Every field is a value the Runner owns; setting
// depth or budget stays with the Runner and the sub-agent sources.
//
// Values only, and wire-serializable, for the same reason Event is (constraint
// A2): a scope may cross a parent/child boundary that a pointer cannot, and
// A7 forbids handing a child a handle to parent state.
//
// Deliberately absent: the signal sinks. They are a control mechanism rather
// than a fact about the run, and exposing one would let an extension raise a
// signal as though it came from a child — the forgery the non-referential
// signal design exists to prevent. The pending Team handoff is out for the
// same reason: it is in-flight control, not scope.
type RunScope struct {
	// Path is the agent ancestry, empty at the top level.
	Path AgentPath `json:"path,omitempty"`

	// CallBudget is the sub-agent invocations remaining under
	// WithAgentCallBudget, shared across the whole tree. -1 when unbounded.
	CallBudget int `json:"callBudget"`

	// Tree is the aggregate TreeBudget consumption for this turn.
	Tree TreeUsage `json:"tree"`
}

// Depth reports how deep in the agent tree the call is, 0 at the top level.
// Shorthand for Path.Depth(), which is the check most callers want.
func (s RunScope) Depth() int { return s.Path.Depth() }

// ScopeFrom reads the run scope a context carries. A context that never
// crossed a Runner returns the zero scope with CallBudget -1, so a caller
// outside a run sees "top level, nothing bounded" rather than a false limit.
//
// Middleware does not need this: ToolCallInfo.Scope is already populated. It
// is here for a tool implementation, which receives only a context.
func ScopeFrom(ctx context.Context) RunScope {
	s := RunScope{Path: agentScope(ctx), CallBudget: -1, Tree: treeBudgetFrom(ctx).usage()}
	if b := agentCallBudget(ctx); b != nil {
		s.CallBudget = int(b.Load())
	}
	return s
}
