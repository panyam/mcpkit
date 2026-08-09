package host

import (
	"context"
	"sync"

	gocurrent "github.com/panyam/gocurrent"
	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/client"
	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
)

// serverSet is the connected MCP servers: the Group that owns their
// connection lifecycle, the clients it drives, the persona-visible mirror of
// their tools, and the interactive token sources that make /login possible.
//
// Held by value; every field is zero-value usable, and a nil group means
// construction failed before the Group existed (Close checks for exactly
// that).
type serverSet struct {
	// group owns the connection lifecycle: async connect, per-server state,
	// backoff reconnect. Its observer registers a server's tools when it
	// becomes ready. See docs/AGENT_SERVER_STATE.md.
	group   *client.Group
	clients []*client.Client

	// tools mirrors the server sources only, with no meta-tools and no
	// sub-agents, so a persona gets a filtered view of the servers without
	// seeing the other personas (which would let it recurse). Nil unless
	// personas are configured.
	tools *agent.MultiSource

	// oauth holds the interactive token source per server id, so LoginServer
	// can force a fresh browser login. Only oauth-typed servers have an entry,
	// and its presence is what CanLogin reports.
	oauth map[string]loginSource
}

// canLogin reports whether the server has an interactive token source.
func (s *serverSet) canLogin(id string) bool { return s.oauth[id] != nil }

// streamSet is the inbound event plumbing: the live subscriptions, the fan-in
// that merges every server's stream into one channel, and the context that
// bounds them all.
//
// Separate from serverSet despite being per-server, because the lifecycles
// differ. Connections come up and go down on the Group's schedule and outlive
// any single subscription; the streams exist only when the async control plane
// is on (metaTools), and Close stops them before the Group. Merging the two
// would put a conditional half inside an unconditional whole.
type streamSet struct {
	mu   sync.Mutex
	subs map[string]*subscription

	calls []*eventsclient.StreamCall

	// fanIn merges every subscription into one channel the consumer drains.
	// Nil when the async control plane is off.
	fanIn *gocurrent.FanIn[agent.IncomingEvent]

	// ctx bounds every subscription. Built before the servers connect, so the
	// ready-observer can subscribe a late server the moment it arrives; stop
	// cancels them all.
	ctx  context.Context
	stop context.CancelFunc
}

// add registers a live subscription. Reports false when the id is already
// taken, which is the caller's cue that the subscription already exists.
func (s *streamSet) add(sub *subscription) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.subs[sub.id]; exists {
		return false
	}
	s.subs[sub.id] = sub
	return true
}

// take removes and returns a subscription, or nil when it is unknown.
func (s *streamSet) take(id string) *subscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.subs[id]
	if !ok {
		return nil
	}
	delete(s.subs, id)
	return sub
}

// has reports whether the id is already subscribed.
func (s *streamSet) has(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.subs[id]
	return ok
}

// all snapshots the live subscriptions.
func (s *streamSet) all() []*subscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*subscription, 0, len(s.subs))
	for _, sub := range s.subs {
		out = append(out, sub)
	}
	return out
}
