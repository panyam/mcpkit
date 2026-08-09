package agent

import (
	"context"
	"strings"
	"testing"
	"time"
)

// latchProvider blocks in Stream until released, so a test can assert the async
// Call returned its ack BEFORE the child finished.
type latchProvider struct {
	release <-chan struct{}
	text    string
}

func (p *latchProvider) Stream(ctx context.Context, req ProviderRequest) (Stream, error) {
	<-p.release
	return &stubStream{deltas: []Delta{{Kind: DeltaText, Text: p.text}, {Kind: DeltaFinish, FinishReason: "stop"}}}, nil
}
func (p *latchProvider) Generate(ctx context.Context, req ProviderRequest) (*ProviderResponse, error) {
	<-p.release
	var acc DeltaAccumulator
	acc.Add(Delta{Kind: DeltaText, Text: p.text})
	acc.Add(Delta{Kind: DeltaFinish, FinishReason: "stop"})
	return acc.Result(), nil
}

// TestAsyncAgentSource_AcksThenDelivers is the core guarantee: Call returns an
// ack immediately (does not block on the child), and OnComplete delivers the
// child's result once it finishes.
func TestAsyncAgentSource_AcksThenDelivers(t *testing.T) {
	release := make(chan struct{})
	child, _ := NewRunner(RunnerConfig{Provider: &latchProvider{release: release, text: "the deep answer"}})

	done := make(chan *TurnResult, 1)
	as, err := NewAsyncAgentSource(AsyncAgentSourceConfig{
		Name: "researcher", Description: "deep research", Runner: child,
		OnComplete: func(name string, result *TurnResult, err error) { done <- result },
	})
	if err != nil {
		t.Fatal(err)
	}

	// Call returns the ack while the child is still latched (not blocked).
	res, err := as.Call(context.Background(), "researcher", map[string]any{"task": "dig in"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(res.Content[0].Text, "started") {
		t.Fatalf("Call should ack immediately, got %+v", res)
	}
	select {
	case <-done:
		t.Fatal("OnComplete fired before the child was released — Call must not block")
	case <-time.After(50 * time.Millisecond):
		// expected: child still latched, no completion yet
	}

	// release the child; OnComplete delivers its result.
	close(release)
	select {
	case result := <-done:
		if result == nil || result.Text != "the deep answer" {
			t.Fatalf("OnComplete result = %+v, want the child's answer", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OnComplete never fired after the child finished")
	}
}

// TestAsyncAgentSource_DepthGuard asserts the depth guard refuses (before spawn)
// and does not start the child.
func TestAsyncAgentSource_DepthGuard(t *testing.T) {
	child, _ := NewRunner(RunnerConfig{Provider: NewStubProvider(StubTurn{Text: "x"})})
	fired := make(chan struct{}, 1)
	as, _ := NewAsyncAgentSource(AsyncAgentSourceConfig{
		Name: "deep", Runner: child, MaxDepth: 1,
		OnComplete: func(string, *TurnResult, error) { fired <- struct{}{} },
	})
	// ctx already at depth 1 (== MaxDepth) → refused.
	ctx := withAgentDepth(context.Background(), 1)
	res, _ := as.Call(ctx, "deep", map[string]any{"task": "go"})
	if !res.IsError || !strings.Contains(res.Content[0].Text, "max depth") {
		t.Fatalf("over-depth spawn should be refused, got %+v", res)
	}
	select {
	case <-fired:
		t.Fatal("a refused spawn must not run the child")
	case <-time.After(50 * time.Millisecond):
	}
}

// TestAsyncAgentSource_ForwardsEvents asserts the child's stream still surfaces
// (SubAgentEvent, scoped/depth) while it runs in the background.
func TestAsyncAgentSource_ForwardsEvents(t *testing.T) {
	child, _ := NewRunner(RunnerConfig{Provider: NewStubProvider(StubTurn{Text: "hi"})})
	events := make(chan SubAgentEvent, 16)
	done := make(chan struct{}, 1)
	as, _ := NewAsyncAgentSource(AsyncAgentSourceConfig{
		Name: "worker", Runner: child,
		OnEvent:    func(e SubAgentEvent) { events <- e },
		OnComplete: func(string, *TurnResult, error) { done <- struct{}{} },
	})
	if _, err := as.Call(context.Background(), "worker", map[string]any{"task": "go"}); err != nil {
		t.Fatal(err)
	}
	<-done
	close(events)
	var sawScoped bool
	for e := range events {
		if e.Scope == "worker" && e.Depth == 1 {
			sawScoped = true
		}
	}
	if !sawScoped {
		t.Fatal("background child events not surfaced scoped to the sub-agent")
	}
}
