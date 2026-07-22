package host

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/core"
)

// TestRendererSubAgentGutter pins the border-left tree gutter (issue 1063 B4):
// depth drives the number of gutter segments, the scope is tagged, and a tool
// call shows name + compact args.
func TestRendererSubAgentGutter(t *testing.T) {
	var out strings.Builder
	r := newRenderer(&out)
	r.plain = true // strip ANSI so the gutter glyph is asserted directly

	r.subAgent(agent.SubAgentEvent{Scope: "research", Depth: 1, Event: agent.Event{
		Kind:     agent.EventToolBegin,
		ToolCall: &agent.ToolCall{Name: "grep", Args: core.NewRawJSON(json.RawMessage(`{"q":"retry"}`))},
	}})
	r.subAgent(agent.SubAgentEvent{Scope: "research/inner", Depth: 2, Event: agent.Event{
		Kind:   agent.EventTurnEnd,
		Result: &agent.TurnResult{Text: "found 3"},
	}})

	got := out.String()
	if !strings.Contains(got, "│ [research] · grep(") || !strings.Contains(got, `"q":"retry"`) {
		t.Fatalf("depth-1 gutter/tool line wrong:\n%q", got)
	}
	if !strings.Contains(got, "│ │ [research/inner] → found 3") {
		t.Fatalf("depth-2 line should carry two gutter segments:\n%q", got)
	}
}

// TestRendererToolCancelledDistinctFromError pins that a user cancel
// renders as an interrupt, not a failure.
func TestRendererToolCancelledDistinctFromError(t *testing.T) {
	var out strings.Builder
	r := newRenderer(&out)
	r.handle(agent.Event{
		Kind:     agent.EventToolCancelled,
		Step:     1,
		ToolCall: &agent.ToolCall{ID: "c1", Name: "slow"},
		Reason:   "cancelled by user",
	})
	got := out.String()
	if !strings.Contains(got, "slow cancelled: cancelled by user") {
		t.Fatalf("tool-cancelled render = %q", got)
	}
	if strings.Contains(got, "failed") {
		t.Fatalf("tool-cancelled rendered as a failure: %q", got)
	}
}
