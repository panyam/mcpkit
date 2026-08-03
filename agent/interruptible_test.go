package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/panyam/mcpkit/core"
)

// twoChildParent builds a parent Runner that calls sub-agents A and B in one
// turn, then answers "re-planned". A is the signalling child; B is supplied by
// the caller (blocking or normal). interruptible toggles the gate.
func twoChildParent(t *testing.T, a, b *AgentSource, interruptible bool) (*Runner, *StubProvider) {
	t.Helper()
	tools := NewMultiSource()
	if err := tools.Add("a-src", a); err != nil {
		t.Fatal(err)
	}
	if err := tools.Add("b-src", b); err != nil {
		t.Fatal(err)
	}
	stub := NewStubProvider(
		StubTurn{ToolCalls: []ToolCall{
			{ID: "ca", Name: a.cfg.Name, Args: core.NewRawJSON(json.RawMessage(`{"task":"go"}`))},
			{ID: "cb", Name: b.cfg.Name, Args: core.NewRawJSON(json.RawMessage(`{"task":"go"}`))},
		}},
		StubTurn{Text: "re-planned"},
	)
	r, err := NewRunner(RunnerConfig{Provider: stub, Tools: tools, Interruptible: interruptible})
	if err != nil {
		t.Fatal(err)
	}
	return r, stub
}

func blockingChild(t *testing.T, name string) *AgentSource {
	t.Helper()
	r, err := NewRunner(RunnerConfig{Provider: blockingProvider{}})
	if err != nil {
		t.Fatal(err)
	}
	as, err := NewAgentSource(AgentSourceConfig{Name: name, Description: "blocks", Runner: r})
	if err != nil {
		t.Fatal(err)
	}
	return as
}

func toolTexts(result *TurnResult) []string {
	var out []string
	for _, m := range result.Messages {
		if m.Role == RoleTool {
			out = append(out, m.Text)
		}
	}
	return out
}

func containsSubstr(ss []string, sub string) bool {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// TestInterruptibleTurn_SignalBreaksBarrier: child A signals mid-fan-out while
// child B blocks forever. In an interruptible turn A's signal cancels B, the
// dispatch returns partial results, and the parent re-plans — so the turn
// completes instead of hanging on B.
func TestInterruptibleTurn_SignalBreaksBarrier(t *testing.T) {
	a := signalingChild(t, "A", "escalate", "found it")
	b := blockingChild(t, "B")
	parent, _ := twoChildParent(t, a, b, true)

	var mu sync.Mutex
	var sawSignal bool
	result, err := parent.Run(context.Background(), []Message{{Role: RoleUser, Text: "go"}}, func(e Event) {
		if e.Kind == EventSignal {
			mu.Lock()
			sawSignal = true
			mu.Unlock()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "re-planned" {
		t.Fatalf("parent final = %q, want it to re-plan after the interrupt", result.Text)
	}
	if !sawSignal {
		t.Fatal("no EventSignal; the interrupt was not signal-driven")
	}
	// B blocks forever, so its only possible terminal result is a cancellation.
	if !containsSubstr(toolTexts(result), "cancelled by user") {
		t.Fatalf("no cancelled sibling among %v; B was not interrupted", toolTexts(result))
	}
}

// TestInterruptibleTurn_NoSignalWaitsForAll: with the gate on but no signal,
// the dispatch still waits for every call — identical to the default barrier.
func TestInterruptibleTurn_NoSignalWaitsForAll(t *testing.T) {
	a, _ := NewAgentSource(AgentSourceConfig{Name: "A", Description: "d", Runner: childRunner(t, StubTurn{Text: "A done"})})
	b, _ := NewAgentSource(AgentSourceConfig{Name: "B", Description: "d", Runner: childRunner(t, StubTurn{Text: "B done"})})
	parent, _ := twoChildParent(t, a, b, true)

	result, err := parent.Run(context.Background(), []Message{{Role: RoleUser, Text: "go"}}, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	texts := toolTexts(result)
	if !containsSubstr(texts, "A done") || !containsSubstr(texts, "B done") {
		t.Fatalf("tool results = %v, want both children to complete", texts)
	}
	if containsSubstr(texts, "cancelled") {
		t.Fatalf("a call was cancelled with no signal: %v", texts)
	}
}

// TestInterruptibleOff_IgnoresSignalForBarrier: with the gate OFF, a child's
// signal does not break the barrier — every call still completes (the signal is
// only recorded/injected). B is a normal child (a blocking one would hang, which
// is exactly the barrier the gate opts out of).
func TestInterruptibleOff_IgnoresSignalForBarrier(t *testing.T) {
	a := signalingChild(t, "A", "escalate", "found it")
	b, _ := NewAgentSource(AgentSourceConfig{Name: "B", Description: "d", Runner: childRunner(t, StubTurn{Text: "B done"})})
	parent, _ := twoChildParent(t, a, b, false)

	var sawSignal bool
	result, err := parent.Run(context.Background(), []Message{{Role: RoleUser, Text: "go"}}, func(e Event) {
		if e.Kind == EventSignal {
			sawSignal = true
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSubstr(toolTexts(result), "B done") {
		t.Fatalf("B did not complete with the gate off: %v", toolTexts(result))
	}
	if containsSubstr(toolTexts(result), "cancelled") {
		t.Fatalf("a call was cancelled with the gate off: %v", toolTexts(result))
	}
	if !sawSignal {
		t.Fatal("signal should still be recorded/injected even with the gate off")
	}
}
