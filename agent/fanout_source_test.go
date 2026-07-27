package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fanBarrier releases all waiters only once n of them have arrived, so a test
// can prove N members ran concurrently: if the fan-out were sequential, the
// first member would block here forever and the test would time out.
type fanBarrier struct {
	mu    sync.Mutex
	count int
	n     int
	ch    chan struct{}
}

func newFanBarrier(n int) *fanBarrier { return &fanBarrier{n: n, ch: make(chan struct{})} }

func (b *fanBarrier) arrive() {
	b.mu.Lock()
	b.count++
	if b.count == b.n {
		close(b.ch)
	}
	b.mu.Unlock()
	<-b.ch
}

// barrierProvider blocks in Stream until the shared barrier releases, then
// emits a fixed text turn.
type barrierProvider struct {
	b    *fanBarrier
	text string
}

func (p *barrierProvider) Stream(ctx context.Context, req ProviderRequest) (Stream, error) {
	p.b.arrive()
	return &stubStream{deltas: []Delta{{Kind: DeltaText, Text: p.text}, {Kind: DeltaFinish, FinishReason: "stop"}}}, nil
}

func (p *barrierProvider) Generate(ctx context.Context, req ProviderRequest) (*ProviderResponse, error) {
	p.b.arrive()
	var acc Accumulator
	acc.Add(Delta{Kind: DeltaText, Text: p.text})
	acc.Add(Delta{Kind: DeltaFinish, FinishReason: "stop"})
	return acc.Result(), nil
}

func mustMemberRunner(t *testing.T, p Provider) *Runner {
	t.Helper()
	r, err := NewRunner(RunnerConfig{Provider: p})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func mustMember(t *testing.T, name string, p Provider) *AgentSource {
	t.Helper()
	s, err := NewAgentSource(AgentSourceConfig{Name: name, Runner: mustMemberRunner(t, p)})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestFanOutRunsMembersConcurrently is the core guarantee: a fan-out dispatches
// to all members at once. Each member blocks on a shared 3-way barrier, so the
// call returns only if all three ran concurrently; a sequential implementation
// would deadlock here.
func TestFanOutRunsMembersConcurrently(t *testing.T) {
	bar := newFanBarrier(3)
	members := []*AgentSource{
		mustMember(t, "a", &barrierProvider{b: bar, text: "from a"}),
		mustMember(t, "b", &barrierProvider{b: bar, text: "from b"}),
		mustMember(t, "c", &barrierProvider{b: bar, text: "from c"}),
	}
	fo, err := NewFanOutSource(FanOutConfig{Name: "team", Members: members})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan string, 1)
	go func() {
		res, _ := fo.Call(context.Background(), "team", map[string]any{"task": "go"})
		done <- toolResultText(res)
	}()
	select {
	case got := <-done:
		for _, want := range []string{"from a", "from b", "from c"} {
			if !strings.Contains(got, want) {
				t.Fatalf("aggregate missing %q: %s", want, got)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fan-out did not run members concurrently (barrier never released) — sequential dispatch")
	}
}

// TestFanOutAggregatesInMemberOrder asserts the combined result is ordered by
// member config order even though members run concurrently.
func TestFanOutAggregatesInMemberOrder(t *testing.T) {
	bar := newFanBarrier(3)
	fo, err := NewFanOutSource(FanOutConfig{Name: "team", Members: []*AgentSource{
		mustMember(t, "first", &barrierProvider{b: bar, text: "alpha"}),
		mustMember(t, "second", &barrierProvider{b: bar, text: "beta"}),
		mustMember(t, "third", &barrierProvider{b: bar, text: "gamma"}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	res, _ := fo.Call(context.Background(), "team", map[string]any{"task": "go"})
	text := toolResultText(res)
	iA, iB, iC := strings.Index(text, "alpha"), strings.Index(text, "beta"), strings.Index(text, "gamma")
	if iA < 0 || iB < 0 || iC < 0 || !(iA < iB && iB < iC) {
		t.Fatalf("aggregate not in member order (a<b<c): %s", text)
	}
	if !strings.Contains(text, "[first]") {
		t.Fatalf("aggregate missing member labels: %s", text)
	}
}

// errProvider fails every call, so its AgentSource surfaces an IsError result.
type errProvider struct{}

func (errProvider) Stream(context.Context, ProviderRequest) (Stream, error) {
	return nil, errors.New("boom")
}
func (errProvider) Generate(context.Context, ProviderRequest) (*ProviderResponse, error) {
	return nil, errors.New("boom")
}

// TestFanOutIsolatesMemberFailure asserts one member failing does not abort the
// others: the fan-out returns, the failed member's section is marked, and the
// healthy members' answers are present.
func TestFanOutIsolatesMemberFailure(t *testing.T) {
	fo, err := NewFanOutSource(FanOutConfig{Name: "team", Members: []*AgentSource{
		mustMember(t, "ok1", NewStubProvider(StubTurn{Text: "one"})),
		mustMember(t, "bad", errProvider{}),
		mustMember(t, "ok2", NewStubProvider(StubTurn{Text: "two"})),
	}})
	if err != nil {
		t.Fatal(err)
	}
	res, callErr := fo.Call(context.Background(), "team", map[string]any{"task": "go"})
	if callErr != nil {
		t.Fatalf("member failure must not be a dispatch error: %v", callErr)
	}
	if res.IsError {
		t.Fatal("the fan-out tool itself should not be IsError when a single member fails")
	}
	text := toolResultText(res)
	if !strings.Contains(text, "[bad (error)]") {
		t.Fatalf("failed member not marked in aggregate: %s", text)
	}
	if !strings.Contains(text, "one") || !strings.Contains(text, "two") {
		t.Fatalf("healthy members missing from aggregate: %s", text)
	}
}

// TestFanOutRespectsCallBudget asserts the shared ctx-threaded budget bounds the
// fan-out: with a budget of 2 and 3 members, exactly two run and one is refused.
func TestFanOutRespectsCallBudget(t *testing.T) {
	fo, err := NewFanOutSource(FanOutConfig{Name: "team", Members: []*AgentSource{
		mustMember(t, "a", NewStubProvider(StubTurn{Text: "ra"})),
		mustMember(t, "b", NewStubProvider(StubTurn{Text: "rb"})),
		mustMember(t, "c", NewStubProvider(StubTurn{Text: "rc"})),
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithAgentCallBudget(context.Background(), 2)
	res, _ := fo.Call(ctx, "team", map[string]any{"task": "go"})
	text := toolResultText(res)
	if n := strings.Count(text, "budget exhausted"); n != 1 {
		t.Fatalf("expected exactly one member refused by budget, got %d: %s", n, text)
	}
}
