package agentsclient

import (
	"context"
	"sync"
	"time"

	"github.com/panyam/mcpkit/experimental/ext/agents"
)

// Cache keeps a host's agents discovery results for as long as the server
// says they stay fresh, following agents-wg#22 § Caching and Freshness:
//
//   - The roster (every agents/list page) is one entry, and each agents/get
//     result is its own entry keyed by agent name. Neither lifetime extends
//     the other.
//   - An entry is fresh for its SEP-2549 ttlMs. Absent or 0 means
//     immediately stale, so by default such a result is fetched again on
//     every call. WithFallbackTTL sets a lifetime for that case instead.
//   - agent_not_found drops that agent's details and the roster, since the
//     roster evidently changed. Retrying is the caller's choice.
//   - invalid_agent_definition is never cached, so the next call asks
//     again.
//   - Invalidate, for notifications/agents/list_changed, drops the roster.
//     The next ListAgents re-lists, and details of agents no longer listed
//     are discarded.
//
// One Cache serves one Client, which is one authorization context, so
// cacheScope private and public are equally safe to keep here. Never share a
// Cache, or its results, across clients with different credentials.
//
// Safe for concurrent use. Fetches run without the lock held, and a fetch
// that started before an Invalidate is not stored. Returned values share
// their slices with the cache, so treat them as read-only.
type Cache struct {
	f           fetcher
	now         func() time.Time
	fallbackTTL time.Duration

	mu      sync.Mutex
	gen     uint64 // bumped by every invalidation; a fetch stores only if unchanged
	roster  *rosterEntry
	details map[string]detailEntry
}

// fetcher is what Cache needs from the wire. *Client satisfies it.
type fetcher interface {
	ListAgentsPage(ctx context.Context, cursor string) (agents.ListAgentsResult, error)
	GetAgent(ctx context.Context, name string) (agents.GetAgentResult, error)
}

type rosterEntry struct {
	cards   []agents.AgentCard
	expires time.Time
}

type detailEntry struct {
	result  agents.GetAgentResult
	expires time.Time
}

// CacheOption configures NewCache.
type CacheOption func(*Cache)

// WithFallbackTTL keeps a result whose ttlMs is absent or 0 for d instead of
// fetching it again on every call. SEP-2549 lets a client cache such a result
// by its own heuristics, and a host that delegates often may want to, at the
// cost of seeing a server-side change up to d late unless it also listens for
// notifications/agents/list_changed. Zero, the default, follows the server.
func WithFallbackTTL(d time.Duration) CacheOption {
	return func(k *Cache) { k.fallbackTTL = d }
}

// WithClock replaces time.Now, for tests.
func WithClock(now func() time.Time) CacheOption {
	return func(k *Cache) { k.now = now }
}

// NewCache returns an empty Cache over c.
func NewCache(c *Client, opts ...CacheOption) *Cache {
	return newCache(c, opts...)
}

func newCache(f fetcher, opts ...CacheOption) *Cache {
	k := &Cache{f: f, now: time.Now, details: map[string]detailEntry{}}
	for _, o := range opts {
		o(k)
	}
	return k
}

// ListAgents returns the roster, from the cache while it is fresh and from
// the server otherwise, walking every page. The roster's lifetime is the
// shortest of its pages'. A re-list also drops cached details for agents it
// no longer contains.
func (k *Cache) ListAgents(ctx context.Context) ([]agents.AgentCard, error) {
	k.mu.Lock()
	if r := k.roster; r != nil && k.now().Before(r.expires) {
		k.mu.Unlock()
		return r.cards, nil
	}
	gen := k.gen
	k.mu.Unlock()

	cards, ttl, err := k.fetchRoster(ctx)
	if err != nil {
		return nil, err
	}

	k.mu.Lock()
	defer k.mu.Unlock()
	if k.gen == gen {
		k.roster = &rosterEntry{cards: cards, expires: k.now().Add(ttl)}
		listed := make(map[string]bool, len(cards))
		for _, c := range cards {
			listed[c.Name] = true
		}
		for name := range k.details {
			if !listed[name] {
				delete(k.details, name)
			}
		}
	}
	return cards, nil
}

func (k *Cache) fetchRoster(ctx context.Context) ([]agents.AgentCard, time.Duration, error) {
	var cards []agents.AgentCard
	var ttl time.Duration
	first := true
	cursor := ""
	seen := map[string]bool{}
	for {
		page, err := k.f.ListAgentsPage(ctx, cursor)
		if err != nil {
			return nil, 0, err
		}
		cards = append(cards, page.Agents...)
		if t := k.lifetime(page.TTLMs); first || t < ttl {
			ttl = t
		}
		first = false
		if page.NextCursor == "" || seen[page.NextCursor] {
			break
		}
		seen[page.NextCursor] = true
		cursor = page.NextCursor
	}
	if cards == nil {
		cards = []agents.AgentCard{}
	}
	return cards, ttl, nil
}

// GetAgent returns one agent's details, from the cache while they are fresh
// and from the server otherwise. Errors are not cached. agent_not_found also
// drops the roster, so the next ListAgents re-lists.
func (k *Cache) GetAgent(ctx context.Context, name string) (agents.GetAgentResult, error) {
	k.mu.Lock()
	if d, ok := k.details[name]; ok && k.now().Before(d.expires) {
		k.mu.Unlock()
		return d.result, nil
	}
	gen := k.gen
	k.mu.Unlock()

	res, err := k.f.GetAgent(ctx, name)

	k.mu.Lock()
	defer k.mu.Unlock()
	if err != nil {
		delete(k.details, name)
		if IsAgentNotFound(err) {
			k.roster = nil
			k.gen++
		}
		return agents.GetAgentResult{}, err
	}
	if k.gen == gen {
		k.details[name] = detailEntry{result: res, expires: k.now().Add(k.lifetime(res.TTLMs))}
	}
	return res, nil
}

// Invalidate drops the roster, so the next ListAgents re-lists. Call it on
// notifications/agents/list_changed. Cached details stay until that re-list
// shows which agents are gone, or until they expire.
func (k *Cache) Invalidate() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.roster = nil
	k.gen++
}

// InvalidateAgent drops one agent's cached details.
func (k *Cache) InvalidateAgent(name string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.details, name)
	k.gen++
}

func (k *Cache) lifetime(ttlMs *int) time.Duration {
	if ttlMs != nil && *ttlMs > 0 {
		return time.Duration(*ttlMs) * time.Millisecond
	}
	return k.fallbackTTL
}
