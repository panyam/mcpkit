package host

import (
	"strings"
	"testing"

	"github.com/panyam/mcpkit/agent"
)

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
