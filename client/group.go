package client

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ConnState is the connection state of a Group member. It is the client-layer
// half of the agent's per-server status (see docs/AGENT_SERVER_STATE.md): a
// consumer (agent host, a script, cmd/testclient, a dashboard) reacts to
// transitions to register/deregister capabilities and to render status.
type ConnState int

const (
	// StateDisabled is a member that has been added but not yet started.
	StateDisabled ConnState = iota
	// StateConnecting is an in-flight connection attempt (initial or a retry).
	StateConnecting
	// StateReady is connected and initialized — capabilities are usable.
	StateReady
	// StateFailed is a connect failure that will be retried with backoff
	// (network refusal, 5xx, EOF). Err holds the last failure.
	StateFailed
	// StateNeedsLogin is a 401/403 rejection: the member needs user action
	// (login) and is NOT auto-retried, so a retry loop can't hammer the server.
	StateNeedsLogin
)

// String renders a ConnState for status output.
func (s ConnState) String() string {
	switch s {
	case StateDisabled:
		return "disabled"
	case StateConnecting:
		return "connecting"
	case StateReady:
		return "ready"
	case StateFailed:
		return "failed"
	case StateNeedsLogin:
		return "needs-login"
	default:
		return "unknown"
	}
}

// StateChange is delivered to a Group observer on every member transition.
type StateChange struct {
	ID    string
	State ConnState
	Err   error // set for StateFailed / StateNeedsLogin
}

// MemberStatus is an ordered snapshot entry from Group.Status, for rendering a
// /servers-style view without racing the live state.
type MemberStatus struct {
	ID       string
	State    ConnState
	Err      error
	Required bool
}

// classifyConnectErr maps a Connect error to the state it should produce. Auth
// rejections (401/403) are terminal-until-user-action (StateNeedsLogin); every
// other error is retryable (StateFailed), reusing the transient/terminal split
// the reconnection path already draws — a "failed" member keeps retrying, which
// is what lets a server that comes up late wire itself in.
func classifyConnectErr(err error) ConnState {
	var authErr *ClientAuthError
	if errors.As(err, &authErr) {
		return StateNeedsLogin
	}
	return StateFailed
}

type groupMember struct {
	id       string
	client   *Client
	required bool

	// guarded by Group.mu
	state ConnState
	err   error
	ready chan struct{} // closed once, on first StateReady (for WaitRequired)
}

// Group manages the connection lifecycle of N clients: it connects them
// concurrently, tracks per-member state, retries failed members with backoff,
// and lets a caller block until the members it marked required are ready. It is
// host-agnostic — the connection concern lives in client/ so any consumer can
// hold a Group, while the reaction to a member becoming ready (registering its
// tools/skills/events) stays with the consumer via an observer.
//
// A Group does not create the clients; the caller builds each Client and hands
// it to Add. Close stops the retry loops and closes the member clients (the
// Group owns their lifetime once Add is called).
type Group struct {
	mu      sync.Mutex
	members map[string]*groupMember
	order   []string

	reqTimeout time.Duration
	minBackoff time.Duration
	maxBackoff time.Duration
	observer   func(StateChange)

	started bool
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// GroupOption configures a Group.
type GroupOption func(*Group)

// WithRequiredTimeout sets how long WaitRequired blocks before failing with the
// required members that never reached ready. Default 30s; 0 means wait
// indefinitely (the "hang until this server is up, however long it takes" case).
func WithRequiredTimeout(d time.Duration) GroupOption {
	return func(g *Group) { g.reqTimeout = d }
}

// WithGroupBackoff sets the exponential backoff bounds for retrying a failed
// member. Defaults: 500ms to 30s.
func WithGroupBackoff(min, max time.Duration) GroupOption {
	return func(g *Group) { g.minBackoff, g.maxBackoff = min, max }
}

// WithObserver registers a callback invoked on every member state transition.
// It is called from the member's own goroutine, so it may be invoked
// concurrently for different members and must be safe for concurrent use; keep
// it quick or hand off, since a slow observer stalls that member's loop.
func WithObserver(fn func(StateChange)) GroupOption {
	return func(g *Group) { g.observer = fn }
}

// NewGroup builds a Group. Add members, then Start.
func NewGroup(opts ...GroupOption) *Group {
	g := &Group{
		members:    map[string]*groupMember{},
		reqTimeout: 30 * time.Second,
		minBackoff: 500 * time.Millisecond,
		maxBackoff: 30 * time.Second,
	}
	for _, o := range opts {
		o(g)
	}
	return g
}

// Add registers c under id. A required member blocks WaitRequired until it is
// ready. Add must be called before Start; a duplicate id is ignored.
func (g *Group) Add(id string, c *Client, required bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.members[id]; ok {
		return
	}
	g.members[id] = &groupMember{id: id, client: c, required: required, state: StateDisabled, ready: make(chan struct{})}
	g.order = append(g.order, id)
}

// Start kicks off an async connect loop per member and returns immediately —
// the caller is usable before any member is ready. Idempotent.
func (g *Group) Start(ctx context.Context) {
	g.mu.Lock()
	if g.started {
		g.mu.Unlock()
		return
	}
	g.started = true
	g.ctx, g.cancel = context.WithCancel(ctx)
	members := make([]*groupMember, 0, len(g.order))
	for _, id := range g.order {
		members = append(members, g.members[id])
	}
	g.mu.Unlock()

	for _, m := range members {
		g.wg.Add(1)
		go g.run(m)
	}
}

// run is one member's connect-and-retry loop. On success it settles at
// StateReady and stops (a live drop after ready is out of scope for phase 1 —
// the client reconnects a dropped live connection internally). On an auth
// rejection it settles at StateNeedsLogin and stops (no hammering). Any other
// failure retries with exponential backoff until the context is cancelled.
func (g *Group) run(m *groupMember) {
	defer g.wg.Done()
	backoff := g.minBackoff
	for {
		if g.ctx.Err() != nil {
			return
		}
		g.set(m, StateConnecting, nil)
		err := m.client.Connect()
		if err == nil {
			g.set(m, StateReady, nil)
			return
		}
		state := classifyConnectErr(err)
		g.set(m, state, err)
		if state == StateNeedsLogin {
			return
		}
		select {
		case <-g.ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > g.maxBackoff {
			backoff = g.maxBackoff
		}
	}
}

// set records a transition and notifies the observer. It closes the member's
// ready channel exactly once when it first reaches ready, which is what
// WaitRequired waits on.
func (g *Group) set(m *groupMember, s ConnState, err error) {
	g.mu.Lock()
	m.state, m.err = s, err
	if s == StateReady {
		select {
		case <-m.ready: // already closed
		default:
			close(m.ready)
		}
	}
	obs := g.observer
	g.mu.Unlock()
	if obs != nil {
		obs(StateChange{ID: m.id, State: s, Err: err})
	}
}

// WaitRequired blocks until every required member has reached ready, the
// context is cancelled, or the required-timeout elapses (returning an error
// naming the members that did not make it). With no required members it returns
// immediately. A zero timeout waits indefinitely.
func (g *Group) WaitRequired(ctx context.Context) error {
	g.mu.Lock()
	var reqs []*groupMember
	for _, id := range g.order {
		if m := g.members[id]; m.required {
			reqs = append(reqs, m)
		}
	}
	timeout := g.reqTimeout
	g.mu.Unlock()

	var deadline <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		deadline = t.C
	}
	for _, m := range reqs {
		select {
		case <-m.ready:
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("client: required servers not ready within %s: %s", timeout, g.notReadyRequired())
		}
	}
	return nil
}

// notReadyRequired lists the ids of required members not yet ready, for the
// WaitRequired timeout message.
func (g *Group) notReadyRequired() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []string
	for _, id := range g.order {
		m := g.members[id]
		if m.required && m.state != StateReady {
			out = append(out, fmt.Sprintf("%s(%s)", id, m.state))
		}
	}
	return strings.Join(out, ", ")
}

// State returns a member's current state and whether the id is known.
func (g *Group) State(id string) (ConnState, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	m, ok := g.members[id]
	if !ok {
		return StateDisabled, false
	}
	return m.state, true
}

// Status returns an ordered snapshot of every member, for a /servers-style view.
func (g *Group) Status() []MemberStatus {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]MemberStatus, 0, len(g.order))
	for _, id := range g.order {
		m := g.members[id]
		out = append(out, MemberStatus{ID: id, State: m.state, Err: m.err, Required: m.required})
	}
	return out
}

// Close stops the connect/retry loops and closes the member clients. It waits
// for the per-member goroutines to exit. Safe to call more than once.
func (g *Group) Close() error {
	g.mu.Lock()
	if g.cancel != nil {
		g.cancel()
	}
	members := make([]*groupMember, 0, len(g.order))
	for _, id := range g.order {
		members = append(members, g.members[id])
	}
	g.mu.Unlock()

	// Close the clients to abort any in-flight Connect (which takes no context),
	// then wait for the goroutines to observe cancellation and exit.
	var firstErr error
	for _, m := range members {
		if err := m.client.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	g.wg.Wait()
	return firstErr
}
