package agentsclient

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/agents"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeFetcher serves a scripted roster and details and counts calls.
type fakeFetcher struct {
	mu        sync.Mutex
	pages     []agents.ListAgentsResult
	details   map[string]agents.GetAgentResult
	errs      map[string]error
	listCalls int
	getCalls  map[string]int
	block     chan struct{} // when set, GetAgent waits on it
}

func newFake(pages ...agents.ListAgentsResult) *fakeFetcher {
	return &fakeFetcher{pages: pages, details: map[string]agents.GetAgentResult{}, errs: map[string]error{}, getCalls: map[string]int{}}
}

func (f *fakeFetcher) ListAgentsPage(_ context.Context, cursor string) (agents.ListAgentsResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if cursor == "" {
		f.listCalls++
		return f.pages[0], nil
	}
	for i, p := range f.pages {
		if p.NextCursor == cursor {
			return f.pages[i+1], nil
		}
	}
	return agents.ListAgentsResult{}, nil
}

func (f *fakeFetcher) GetAgent(_ context.Context, name string) (agents.GetAgentResult, error) {
	f.mu.Lock()
	f.getCalls[name]++
	block := f.block
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.errs[name]; err != nil {
		return agents.GetAgentResult{}, err
	}
	return f.details[name], nil
}

func (f *fakeFetcher) counts(name string) (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listCalls, f.getCalls[name]
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func ms(n int) *int { return &n }

func cards(names ...string) []agents.AgentCard {
	out := make([]agents.AgentCard, len(names))
	for i, n := range names {
		out[i] = agents.AgentCard{Name: n}
	}
	return out
}

func rpcErr(code int, reason string) error {
	return &client.RPCError{Code: code, Message: reason, Data: map[string]any{"reason": reason}}
}

func newTestCache(f *fakeFetcher, opts ...CacheOption) (*Cache, *clock) {
	c := &clock{t: time.Unix(1000, 0)}
	return newCache(f, append([]CacheOption{WithClock(c.now)}, opts...)...), c
}

func TestCacheRosterServedUntilTTL(t *testing.T) {
	f := newFake(agents.ListAgentsResult{Agents: cards("a"), TTLMs: ms(1000)})
	k, clk := newTestCache(f)
	ctx := context.Background()

	_, err := k.ListAgents(ctx)
	require.NoError(t, err)
	clk.advance(999 * time.Millisecond)
	_, _ = k.ListAgents(ctx)
	lists, _ := f.counts("")
	assert.Equal(t, 1, lists, "fresh roster must come from the cache")

	clk.advance(time.Millisecond)
	_, _ = k.ListAgents(ctx)
	lists, _ = f.counts("")
	assert.Equal(t, 2, lists, "expired roster must be fetched again")
}

func TestCacheRosterLifetimeIsShortestPage(t *testing.T) {
	f := newFake(
		agents.ListAgentsResult{Agents: cards("a"), NextCursor: "p2", TTLMs: ms(5000)},
		agents.ListAgentsResult{Agents: cards("b"), TTLMs: ms(1000)},
	)
	k, clk := newTestCache(f)
	ctx := context.Background()

	got, err := k.ListAgents(ctx)
	require.NoError(t, err)
	assert.Equal(t, cards("a", "b"), got)
	clk.advance(1500 * time.Millisecond)
	_, _ = k.ListAgents(ctx)
	lists, _ := f.counts("")
	assert.Equal(t, 2, lists, "the 1000ms page bounds the whole roster")
}

func TestCacheNoTTLMeansRefetch(t *testing.T) {
	for name, ttl := range map[string]*int{"absent": nil, "zero": ms(0)} {
		t.Run(name, func(t *testing.T) {
			f := newFake(agents.ListAgentsResult{Agents: cards("a"), TTLMs: ttl})
			f.details["a"] = agents.GetAgentResult{Agent: "a", TTLMs: ttl}
			k, _ := newTestCache(f)
			ctx := context.Background()
			for i := 0; i < 3; i++ {
				_, _ = k.ListAgents(ctx)
				_, _ = k.GetAgent(ctx, "a")
			}
			lists, gets := f.counts("a")
			assert.Equal(t, 3, lists)
			assert.Equal(t, 3, gets)
		})
	}
}

// SEP-2549 treats an absent ttlMs and an explicit 0 alike, so the fallback
// applies to both.
func TestCacheFallbackTTL(t *testing.T) {
	for name, ttl := range map[string]*int{"absent": nil, "zero": ms(0)} {
		t.Run(name, func(t *testing.T) { testFallbackTTL(t, ttl) })
	}
}

func testFallbackTTL(t *testing.T, ttl *int) {
	f := newFake(agents.ListAgentsResult{Agents: cards("a"), TTLMs: ttl})
	f.details["a"] = agents.GetAgentResult{Agent: "a", TTLMs: ttl}
	k, clk := newTestCache(f, WithFallbackTTL(time.Minute))
	ctx := context.Background()

	_, _ = k.ListAgents(ctx)
	_, _ = k.GetAgent(ctx, "a")
	clk.advance(59 * time.Second)
	_, _ = k.ListAgents(ctx)
	_, _ = k.GetAgent(ctx, "a")
	lists, gets := f.counts("a")
	assert.Equal(t, []int{1, 1}, []int{lists, gets}, "both kept for the fallback minute")

	clk.advance(time.Second)
	_, _ = k.GetAgent(ctx, "a")
	_, gets = f.counts("a")
	assert.Equal(t, 2, gets)
}

// The roster's TTL does not extend an agent's details, nor the reverse.
func TestCacheDetailsAndRosterExpireIndependently(t *testing.T) {
	f := newFake(agents.ListAgentsResult{Agents: cards("a"), TTLMs: ms(10_000)})
	f.details["a"] = agents.GetAgentResult{Agent: "a", TTLMs: ms(1000)}
	k, clk := newTestCache(f)
	ctx := context.Background()

	_, _ = k.ListAgents(ctx)
	_, _ = k.GetAgent(ctx, "a")
	clk.advance(2 * time.Second)
	_, _ = k.ListAgents(ctx)
	_, _ = k.GetAgent(ctx, "a")
	lists, gets := f.counts("a")
	assert.Equal(t, 1, lists, "roster still fresh")
	assert.Equal(t, 2, gets, "details expired on their own TTL")
}

func TestCacheAgentNotFoundDropsDetailsAndRoster(t *testing.T) {
	f := newFake(agents.ListAgentsResult{Agents: cards("a"), TTLMs: ms(60_000)})
	f.details["a"] = agents.GetAgentResult{Agent: "a", TTLMs: ms(1)}
	k, clk := newTestCache(f)
	ctx := context.Background()

	_, _ = k.ListAgents(ctx)
	_, _ = k.GetAgent(ctx, "a")
	clk.advance(time.Second)
	f.errs["a"] = rpcErr(core.ErrCodeInvalidParams, agents.ReasonAgentNotFound)

	_, err := k.GetAgent(ctx, "a")
	require.True(t, IsAgentNotFound(err))
	_, _ = k.ListAgents(ctx)
	lists, _ := f.counts("a")
	assert.Equal(t, 2, lists, "agent_not_found must force a re-list despite the roster TTL")
}

func TestCacheInvalidDefinitionIsNotCached(t *testing.T) {
	f := newFake(agents.ListAgentsResult{Agents: cards("a")})
	f.errs["a"] = rpcErr(core.ErrCodeInternal, agents.ReasonInvalidAgentDefinition)
	k, _ := newTestCache(f, WithFallbackTTL(time.Hour))
	ctx := context.Background()

	_, err := k.GetAgent(ctx, "a")
	require.True(t, IsInvalidAgentDefinition(err))
	delete(f.errs, "a")
	f.details["a"] = agents.GetAgentResult{Agent: "a"}
	got, err := k.GetAgent(ctx, "a")
	require.NoError(t, err)
	assert.Equal(t, "a", got.Agent, "a fixed server is seen on the next call")
}

func TestCacheInvalidatePrunesVanishedAgents(t *testing.T) {
	f := newFake(agents.ListAgentsResult{Agents: cards("a", "b"), TTLMs: ms(60_000)})
	f.details["a"] = agents.GetAgentResult{Agent: "a", TTLMs: ms(60_000)}
	f.details["b"] = agents.GetAgentResult{Agent: "b", TTLMs: ms(60_000)}
	k, _ := newTestCache(f)
	ctx := context.Background()

	_, _ = k.ListAgents(ctx)
	_, _ = k.GetAgent(ctx, "a")
	_, _ = k.GetAgent(ctx, "b")

	f.pages[0].Agents = cards("a")
	k.Invalidate()
	got, err := k.ListAgents(ctx)
	require.NoError(t, err)
	assert.Equal(t, cards("a"), got)

	_, _ = k.GetAgent(ctx, "a")
	_, _ = k.GetAgent(ctx, "b")
	_, getsA := f.counts("a")
	_, getsB := f.counts("b")
	assert.Equal(t, 1, getsA, "a is still listed, so its details are kept")
	assert.Equal(t, 2, getsB, "b vanished, so its details were dropped")
}

// A fetch that was in flight when Invalidate ran returns its result but must
// not store it, or a host would keep pre-change details for a whole TTL.
func TestCacheInFlightFetchAcrossInvalidateIsNotStored(t *testing.T) {
	f := newFake(agents.ListAgentsResult{Agents: cards("a")})
	f.details["a"] = agents.GetAgentResult{Agent: "a", TTLMs: ms(60_000)}
	f.block = make(chan struct{})
	k, _ := newTestCache(f)
	ctx := context.Background()

	done := make(chan struct{})
	go func() { _, _ = k.GetAgent(ctx, "a"); close(done) }()
	require.Eventually(t, func() bool { _, g := f.counts("a"); return g == 1 }, time.Second, time.Millisecond)
	k.Invalidate()
	close(f.block)
	<-done

	f.mu.Lock()
	f.block = nil
	f.mu.Unlock()
	_, _ = k.GetAgent(ctx, "a")
	_, gets := f.counts("a")
	assert.Equal(t, 2, gets)
}
