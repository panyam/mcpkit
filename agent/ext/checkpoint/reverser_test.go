package checkpoint

import (
	"context"
	"testing"

	"github.com/panyam/mcpkit/agent"
)

// TestCompensateAloneIsNotReversible is the load-bearing distinction. A tool
// that can only offer a compensating action has NOT earned automatic
// approval: deleting an issue is a new call with its own consequences, not an
// inverse of creating one. Flipping this would let a tool auto-approve on the
// strength of an offset nobody verified.
func TestCompensateAloneIsNotReversible(t *testing.T) {
	r := Reversal{Compensate: &agent.ToolCall{Name: "delete_issue"}}
	if r.Reversible() {
		t.Fatal("a compensation-only reversal must not count as reversible")
	}
	if r.IsZero() {
		t.Fatal("a compensation is still something to offer, so not zero")
	}
}

func TestReversalStates(t *testing.T) {
	restore := func(context.Context) error { return nil }
	cases := map[string]struct {
		rev              Reversal
		zero, reversible bool
	}{
		"nothing offered": {Reversal{}, true, false},
		"restore only":    {Reversal{Restore: restore}, false, true},
		"compensate only": {Reversal{Compensate: &agent.ToolCall{Name: "x"}}, false, false},
		"both":            {Reversal{Restore: restore, Compensate: &agent.ToolCall{Name: "x"}}, false, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.rev.IsZero(); got != tc.zero {
				t.Errorf("IsZero() = %v, want %v", got, tc.zero)
			}
			if got := tc.rev.Reversible(); got != tc.reversible {
				t.Errorf("Reversible() = %v, want %v", got, tc.reversible)
			}
		})
	}
}
