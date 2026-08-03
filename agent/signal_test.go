package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/panyam/mcpkit/core"
)

func TestSignal_JSONRoundTrip(t *testing.T) {
	sig := Signal{
		Kind:   SignalCustom,
		Name:   "quorum",
		Note:   "2 of 3 agreed",
		Data:   core.NewRawJSON(json.RawMessage(`{"votes":2}`)),
		Source: "review/checker",
	}
	data, err := json.Marshal(sig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Signal
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	data2, err := json.Marshal(back)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if string(data) != string(data2) {
		t.Fatalf("round-trip drift:\n got %s\nwant %s", data2, data)
	}
}

func TestRaiseSignal_NoParentReturnsFalse(t *testing.T) {
	if RaiseSignal(context.Background(), Signal{Kind: SignalEscalate}) {
		t.Fatal("RaiseSignal with no parent sink should return false")
	}
}

func TestSignalSource_TopLevelGraceful(t *testing.T) {
	fs := NewSignalSource()
	res, err := fs.Call(context.Background(), SignalParentToolName, map[string]any{"kind": "escalate", "note": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(res.Content[0].Text, "no parent") {
		t.Fatalf("top-level signal_parent = %+v, want a graceful 'no parent' result", res)
	}
}

func TestSignalKind_Interrupts(t *testing.T) {
	cases := []struct {
		kind SignalKind
		want bool
	}{
		{SignalPreempt, true},
		{SignalEscalate, true},
		{SignalCustom, false},
		{SignalKind("unknown"), false},
	}
	for _, c := range cases {
		if got := c.kind.interrupts(); got != c.want {
			t.Errorf("%q.interrupts() = %v, want %v", c.kind, got, c.want)
		}
	}
}

func TestSignalSource_AcceptsPreempt(t *testing.T) {
	fs := NewSignalSource()
	res, err := fs.Call(context.Background(), SignalParentToolName, map[string]any{"kind": "preempt", "note": "enough"})
	if err != nil {
		t.Fatal(err)
	}
	// No parent sink here, so it reports "no parent" — but crucially NOT an
	// error, which proves "preempt" is an accepted kind (it passed validation).
	if res.IsError {
		t.Fatalf("preempt kind rejected: %+v", res)
	}
	if !strings.Contains(res.Content[0].Text, "no parent") {
		t.Fatalf("preempt result = %q, want the graceful no-parent path", res.Content[0].Text)
	}
}

func TestSignalSource_RejectsUnknownKind(t *testing.T) {
	fs := NewSignalSource()
	res, err := fs.Call(context.Background(), SignalParentToolName, map[string]any{"kind": "bogus", "note": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content[0].Text, "unknown signal kind") {
		t.Fatalf("unknown kind = %+v, want an IsError result naming the bad kind", res)
	}
}

// signalingChild builds an AgentSource whose child raises signal_parent(kind,
// note) on its first turn, then answers. It is the reusable "a sub-agent that
// signals up" fixture.
func signalingChild(t *testing.T, name, kind, note string) *AgentSource {
	t.Helper()
	child, err := NewRunner(RunnerConfig{
		Provider: NewStubProvider(
			StubTurn{ToolCalls: []ToolCall{{ID: "s1", Name: SignalParentToolName,
				Args: core.NewRawJSON(json.RawMessage(`{"kind":"` + kind + `","note":"` + note + `"}`))}}},
			StubTurn{Text: name + " done"},
		),
		Tools: NewSignalSource(),
	})
	if err != nil {
		t.Fatal(err)
	}
	as, err := NewAgentSource(AgentSourceConfig{Name: name, Description: "d", Runner: child})
	if err != nil {
		t.Fatal(err)
	}
	return as
}

func parentOver(t *testing.T, child *AgentSource, policy SignalPolicy) (*Runner, *StubProvider) {
	t.Helper()
	tools := NewMultiSource()
	if err := tools.Add("child-src", child); err != nil {
		t.Fatal(err)
	}
	stub := NewStubProvider(
		StubTurn{ToolCalls: []ToolCall{{ID: "c1", Name: child.cfg.Name,
			Args: core.NewRawJSON(json.RawMessage(`{"task":"go"}`))}}},
		StubTurn{Text: "parent done"},
	)
	r, err := NewRunner(RunnerConfig{Provider: stub, Tools: tools, SignalPolicy: policy})
	if err != nil {
		t.Fatal(err)
	}
	return r, stub
}

// TestUpwardSignal_ReachesParentAndInjects: with no policy, a child's escalate
// reaches the parent, emits EventSignal, and is injected into the parent's next
// step as a RoleSystem note the parent model sees.
func TestUpwardSignal_ReachesParentAndInjects(t *testing.T) {
	child := signalingChild(t, "worker", "escalate", "found it")
	parent, stub := parentOver(t, child, nil)

	var mu sync.Mutex
	var signals []Signal
	res, err := parent.Run(context.Background(), []Message{{Role: RoleUser, Text: "go"}}, func(e Event) {
		if e.Kind == EventSignal {
			mu.Lock()
			signals = append(signals, *e.Signal)
			mu.Unlock()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "parent done" {
		t.Fatalf("parent final = %q, want it to continue after the signal", res.Text)
	}
	if len(signals) != 1 || signals[0].Kind != SignalEscalate || signals[0].Source != "worker" {
		t.Fatalf("EventSignal = %+v, want one escalate from 'worker'", signals)
	}
	// The parent's second model call sees the signal injected as a RoleSystem note.
	fed := stub.Requests()[1].Messages
	var injected bool
	for _, m := range fed {
		if m.Role == RoleSystem && strings.Contains(m.Text, "escalate") && strings.Contains(m.Text, "found it") {
			injected = true
		}
	}
	if !injected {
		t.Fatalf("signal not injected into the parent's next step: %+v", fed)
	}
}

// TestUpwardSignal_AbortOnEscalate: the built-in policy ends the parent turn
// when a child escalates, folding the note into ErrSignalAbort.
func TestUpwardSignal_AbortOnEscalate(t *testing.T) {
	child := signalingChild(t, "worker", "escalate", "found it")
	parent, stub := parentOver(t, child, AbortOnEscalate)

	_, err := parent.Run(context.Background(), []Message{{Role: RoleUser, Text: "go"}}, func(Event) {})
	if !errors.Is(err, ErrSignalAbort) {
		t.Fatalf("err = %v, want ErrSignalAbort", err)
	}
	if !strings.Contains(err.Error(), "found it") {
		t.Fatalf("abort reason = %v, want the child's note", err)
	}
	// The parent never reached its second turn (it aborted after the signal).
	if n := len(stub.Requests()); n != 1 {
		t.Fatalf("parent made %d model calls, want 1 (aborted before continuing)", n)
	}
}

// TestUpwardSignal_NoSignalUnchanged: a sub-agent call with no signal leaves
// the parent's next step free of any injected note and emits no EventSignal.
func TestUpwardSignal_NoSignalUnchanged(t *testing.T) {
	quietChild, _ := NewRunner(RunnerConfig{Provider: NewStubProvider(StubTurn{Text: "quiet"})})
	as, _ := NewAgentSource(AgentSourceConfig{Name: "worker", Description: "d", Runner: quietChild})
	parent, stub := parentOver(t, as, AbortOnEscalate)

	var sawSignal bool
	if _, err := parent.Run(context.Background(), []Message{{Role: RoleUser, Text: "go"}}, func(e Event) {
		if e.Kind == EventSignal {
			sawSignal = true
		}
	}); err != nil {
		t.Fatal(err)
	}
	if sawSignal {
		t.Fatal("EventSignal emitted with no child signal")
	}
	for _, m := range stub.Requests()[1].Messages {
		if m.Role == RoleSystem && strings.Contains(m.Text, "Sub-agent signals") {
			t.Fatalf("injected a signal note with no signal: %+v", m)
		}
	}
}

// TestUpwardSignal_NestedReachesImmediateParent: a grandchild's signal reaches
// its immediate parent (the middle agent), NOT the top — proving the sink is
// scoped to the spawner, not shared across the tree.
func TestUpwardSignal_NestedReachesImmediateParent(t *testing.T) {
	var topRec, midRec recorder

	grandchild := signalingChild(t, "grandchild", "escalate", "deep")
	midTools := NewMultiSource()
	if err := midTools.Add("gc-src", grandchild); err != nil {
		t.Fatal(err)
	}
	middleRunner, err := NewRunner(RunnerConfig{
		Provider: NewStubProvider(
			StubTurn{ToolCalls: []ToolCall{{ID: "m1", Name: "grandchild",
				Args: core.NewRawJSON(json.RawMessage(`{"task":"go"}`))}}},
			StubTurn{Text: "middle done"},
		),
		Tools:        midTools,
		SignalPolicy: midRec.policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	middle, _ := NewAgentSource(AgentSourceConfig{Name: "middle", Description: "d", Runner: middleRunner})

	top, _ := parentOver(t, middle, topRec.policy)
	if _, err := top.Run(context.Background(), []Message{{Role: RoleUser, Text: "go"}}, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	if len(midRec.sigs) != 1 || midRec.sigs[0].Source != "middle/grandchild" {
		t.Fatalf("middle recorded %+v, want one escalate from 'middle/grandchild'", midRec.sigs)
	}
	if len(topRec.sigs) != 0 {
		t.Fatalf("top recorded %+v, want none (signal is scoped to the immediate parent)", topRec.sigs)
	}
}

type recorder struct {
	mu   sync.Mutex
	sigs []Signal
}

func (r *recorder) policy(signals []Signal) SignalAction {
	r.mu.Lock()
	r.sigs = append(r.sigs, signals...)
	r.mu.Unlock()
	return SignalAction{}
}
