package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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
	return twoChildParentCfg(t, a, b, RunnerConfig{Interruptible: interruptible})
}

// twoChildParentGranting builds an interruptible parent whose PreemptGrant
// honors preempts per grant — the "parent authorises the preemption" fixture.
func twoChildParentGranting(t *testing.T, a, b *AgentSource, grant func(Signal) bool) (*Runner, *StubProvider) {
	t.Helper()
	return twoChildParentCfg(t, a, b, RunnerConfig{Interruptible: true, PreemptGrant: grant})
}

func twoChildParentCfg(t *testing.T, a, b *AgentSource, cfg RunnerConfig) (*Runner, *StubProvider) {
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
	cfg.Provider = stub
	cfg.Tools = tools
	r, err := NewRunner(cfg)
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

// TestInterruptibleTurn_PreemptGrantedBreaksBarrier: when the parent grants
// preemption (PreemptGrant returns true), a child's preempt breaks the barrier
// like escalate — the blocking child B is cancelled and the parent re-plans.
func TestInterruptibleTurn_PreemptGrantedBreaksBarrier(t *testing.T) {
	a := signalingChild(t, "A", "preempt", "enough")
	b := blockingChild(t, "B")
	parent, _ := twoChildParentGranting(t, a, b, func(Signal) bool { return true })

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
		t.Fatalf("parent final = %q, want it to re-plan after the granted preempt", result.Text)
	}
	if !sawSignal {
		t.Fatal("no EventSignal; the interrupt was not signal-driven")
	}
	// B blocks forever, so its only possible terminal result is a cancellation.
	if !containsSubstr(toolTexts(result), "cancelled by user") {
		t.Fatalf("no cancelled sibling among %v; B was not preempted", toolTexts(result))
	}
}

// TestInterruptibleTurn_PreemptNotGrantedDoesNotBreak: with no PreemptGrant (the
// default), a child's preempt is advisory only — it must NOT break the barrier
// or cancel siblings. B runs to completion; the preempt is still injected. This
// is the safe-by-default guard: a child cannot unilaterally kill its siblings.
func TestInterruptibleTurn_PreemptNotGrantedDoesNotBreak(t *testing.T) {
	release := make(chan struct{})
	a := signalingChild(t, "A", "preempt", "enough")
	b := releasableChild(t, "B", release)
	parent, _ := twoChildParent(t, a, b, true) // interruptible, but no grant

	var mu sync.Mutex
	var sawSignal bool
	var once sync.Once
	result, err := parent.Run(context.Background(), []Message{{Role: RoleUser, Text: "go"}}, func(e Event) {
		if e.Kind == EventSignal {
			mu.Lock()
			sawSignal = true
			mu.Unlock()
		}
		if e.Kind == EventToolEnd && e.ToolCall != nil && e.ToolCall.ID == "ca" {
			once.Do(func() { close(release) })
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	texts := toolTexts(result)
	if containsSubstr(texts, "cancelled by user") {
		t.Fatalf("B was cancelled by an ungranted preempt: %v", texts)
	}
	if !containsSubstr(texts, "B done") {
		t.Fatalf("B did not complete: %v", texts)
	}
	mu.Lock()
	defer mu.Unlock()
	if !sawSignal {
		t.Fatal("preempt should still be recorded/injected even without a grant")
	}
}

// TestInterruptibleTurn_CustomDoesNotBreakBarrier: a custom (FYI) signal is
// collected and injected but must NOT break the interruptible barrier — the
// other in-flight sibling runs to completion, never cancelled. B is released
// deterministically once A's call has ended (its parent-level tool-end), so the
// only way B shows "cancelled by user" is a wrongful barrier break. Fails before
// kind-aware breaking (custom used to cancel B), passes after.
func TestInterruptibleTurn_CustomDoesNotBreakBarrier(t *testing.T) {
	release := make(chan struct{})
	a := signalingChild(t, "A", "custom", "fyi")
	b := releasableChild(t, "B", release)
	parent, _ := twoChildParent(t, a, b, true)

	var mu sync.Mutex
	var sawSignal bool
	var once sync.Once
	result, err := parent.Run(context.Background(), []Message{{Role: RoleUser, Text: "go"}}, func(e Event) {
		if e.Kind == EventSignal {
			mu.Lock()
			sawSignal = true
			mu.Unlock()
		}
		// A's call ("ca") has ended, so under the fix dispatch is now waiting on
		// B. Release B so the turn finishes; if custom had wrongly broken the
		// barrier, B was already cancelled before this fired.
		if e.Kind == EventToolEnd && e.ToolCall != nil && e.ToolCall.ID == "ca" {
			once.Do(func() { close(release) })
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	texts := toolTexts(result)
	if containsSubstr(texts, "cancelled by user") {
		t.Fatalf("B was cancelled by a custom (FYI) signal: %v", texts)
	}
	if !containsSubstr(texts, "B done") {
		t.Fatalf("B did not complete: %v", texts)
	}
	mu.Lock()
	defer mu.Unlock()
	if !sawSignal {
		t.Fatal("custom signal should still be recorded/injected")
	}
}

// releasableChild is a sub-agent whose single turn blocks until release is
// closed (then it answers "<name> done") or its ctx is cancelled (then it
// errors) — the test controls whether it finishes or is cancelled, so a barrier
// break is observable without a timed sleep.
func releasableChild(t *testing.T, name string, release <-chan struct{}) *AgentSource {
	t.Helper()
	r, err := NewRunner(RunnerConfig{Provider: releasableProvider{release: release, text: name + " done"}})
	if err != nil {
		t.Fatal(err)
	}
	as, err := NewAgentSource(AgentSourceConfig{Name: name, Description: "d", Runner: r})
	if err != nil {
		t.Fatal(err)
	}
	return as
}

type releasableProvider struct {
	release <-chan struct{}
	text    string
}

func (p releasableProvider) Stream(ctx context.Context, req ProviderRequest) (Stream, error) {
	return &releasableStream{ctx: ctx, release: p.release, deltas: StubTurn{Text: p.text}.deltas()}, nil
}

func (p releasableProvider) Generate(ctx context.Context, req ProviderRequest) (*ProviderResponse, error) {
	return nil, errors.New("unused")
}

type releasableStream struct {
	ctx     context.Context
	release <-chan struct{}
	deltas  []Delta
	i       int
	gated   bool
}

func (s *releasableStream) Recv() (Delta, error) {
	if !s.gated {
		select {
		case <-s.release:
		case <-s.ctx.Done():
			return Delta{}, s.ctx.Err()
		}
		s.gated = true
	}
	if s.i >= len(s.deltas) {
		return Delta{}, io.EOF
	}
	d := s.deltas[s.i]
	s.i++
	return d, nil
}

func (s *releasableStream) Close() error { return nil }
