package agent

import (
	"github.com/panyam/mcpkit/core"
)

// EventKind discriminates Event payloads. The vocabulary is the surfaces
// contract from docs/AGENT_DESIGN.md: every kind is emitted in-process and
// must project 1:1 onto a wire.
type EventKind string

// Event kinds, in the order a typical turn emits them. Thinking markers wrap
// contiguous reasoning deltas within one step; tool events may interleave
// across parallel calls of the same step, but tool-begin always precedes its
// call's tool-end, tool-error, tool-denied, or tool-cancelled.
const (
	EventTurnBegin     EventKind = "turn-begin"
	EventThinkingBegin EventKind = "thinking-begin"
	EventThinkingDelta EventKind = "thinking-delta"
	EventThinkingEnd   EventKind = "thinking-end"
	EventTextDelta     EventKind = "text-delta"
	EventToolBegin     EventKind = "tool-begin"
	EventToolEnd       EventKind = "tool-end"
	EventToolError     EventKind = "tool-error"
	EventToolDenied    EventKind = "tool-denied"
	// EventToolCancelled marks a call the user cancelled mid-flight via
	// a TurnRequest Control. Distinct from tool-error (the tool did not
	// fail; the user stopped it) so surfaces can render an interrupt
	// differently from a failure. Reason carries the model-visible
	// feedback text.
	EventToolCancelled EventKind = "tool-cancelled"
	// EventToolUnavailable marks a call whose backing server was not reachable
	// (ErrNotAvailableNow): the tool exists but its server is down right now.
	// Distinct from tool-error (nothing failed on the server; it just isn't
	// there yet) so a surface can render it as a transient miss and evals keyed
	// on Error don't count it. Reason carries the model-visible feedback text;
	// the turn continues so the model can retry, route around it, or tell the
	// user. See docs/AGENT_SERVER_STATE.md.
	EventToolUnavailable EventKind = "tool-unavailable"
	// EventCompaction marks that a Compactor rewrote the turn's history
	// before the first model call (the head summarized, a recent tail kept
	// verbatim). Emitted only when compaction actually fired; Compaction
	// carries the before/after message counts. Surfaces can render a
	// "compacted context" note; evals can assert it happened.
	EventCompaction EventKind = "compaction"
	EventTurnEnd    EventKind = "turn-end"
	EventError      EventKind = "error"
)

// Event is one increment of a running turn, the payload surfaces consume
// (constraint A2: JSON-tagged, stable kind, no Go-only fields). Events carry
// no turn or session identity on purpose: in-process the emit closure is
// scoped to one Run call, and wire layers wrap events in their own envelope
// (session id, turn id, sequence) rather than the module pre-committing an
// ID scheme.
type Event struct {
	Kind EventKind `json:"kind"`

	// Step is the 1-based loop step for step-scoped kinds (deltas, tool
	// events). Zero on turn-begin, turn-end, and error.
	Step int `json:"step,omitempty"`

	// Text carries the fragment for text-delta and thinking-delta.
	Text string `json:"text,omitempty"`

	// ToolCall identifies the call for tool-begin, tool-end, and
	// tool-error.
	ToolCall *ToolCall `json:"toolCall,omitempty"`

	// ToolResult is the outcome on tool-end (including IsError results;
	// tool-error is reserved for dispatch failures).
	ToolResult *core.ToolResult `json:"toolResult,omitempty"`

	// Error is the failure description on tool-error and error. A string,
	// not an error value, so the event crosses wires unchanged.
	Error string `json:"error,omitempty"`

	// Reason is the human-readable justification on tool-denied (why the
	// approval policy refused the call) and tool-cancelled (the user
	// stopped the call). Distinct from Error because both are outcomes
	// of someone's decision, not dispatch failures; neither carries a
	// ToolResult.
	Reason string `json:"reason,omitempty"`

	// Result is the completed turn on turn-end.
	Result *TurnResult `json:"result,omitempty"`

	// Compaction carries the before/after message counts on compaction.
	Compaction *CompactionInfo `json:"compaction,omitempty"`
}

// CompactionInfo reports what a compaction pass did: the message count
// before and after the head was summarized. After < Before whenever the
// event fires.
type CompactionInfo struct {
	Before int `json:"before"`
	After  int `json:"after"`
}
