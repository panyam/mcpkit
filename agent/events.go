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
// call's tool-end, tool-error, or tool-denied.
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
	EventTurnEnd       EventKind = "turn-end"
	EventError         EventKind = "error"
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

	// Reason is the human-readable justification on tool-denied: why the
	// approval policy refused the call. Distinct from Error because a
	// denial is a policy outcome, not a dispatch failure; the call never
	// ran, so there is no ToolResult.
	Reason string `json:"reason,omitempty"`

	// Result is the completed turn on turn-end.
	Result *TurnResult `json:"result,omitempty"`
}
