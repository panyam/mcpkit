package events

// Poll-lease table — in-memory soft state tracking which (principal,
// eventName, params) tuples have recent poll activity, with TTL-based
// expiry and lifecycle callbacks. Foundation for η-3's poll-mode
// on_subscribe / on_unsubscribe wiring (η-1).
//
// Spec §"Server SDK Guidance" → "Unsubscribe timing by mode" L707-715:
// "the SDK treats poll subscriptions as leased. The lease is keyed on
// `(principal-or-null, eventName, canonicalHash(params))`... The lease
// window is SDK-configurable and SHOULD default to a small multiple of
// the server's typical nextPollSeconds."
//
// Why a separate table rather than reusing WebhookRegistry's TTL state:
// the registry is keyed on (principal, url, name, params) — webhook has
// a delivery URL, poll does not. Different identity tuple → different
// store. Both use the same canonical-JSON-of-params building block from
// identity.go.

import (
	"log"
	"strings"
	"sync"
	"time"
)

const (
	defaultPollLeaseTTL           = 5 * time.Minute // spec L711 example
	defaultPollLeaseSweepInterval = 30 * time.Second
)

// PollLeaseHook is the callback signature for OnCreate / OnExpire. The
// SDK invokes it with the lease's identity tuple so authors (typically
// via the η-3 wiring that turns these into on_subscribe / on_unsubscribe
// calls) can provision and tear down upstream resources.
//
// Hooks fire from the goroutine that called Touch (OnCreate) or the
// background sweep goroutine (OnExpire). Authors that need to do
// blocking work should not be afraid to — the calling sites are
// already async.
type PollLeaseHook func(principal, eventName string, params map[string]any)

// PollLeaseOption configures a PollLeaseTable at construction time.
type PollLeaseOption func(*PollLeaseTable)

// WithPollLeaseTTL overrides the lease TTL. Default is 5 minutes
// (spec L711 example). A new poll within the TTL window renews the
// lease (no OnCreate fires); a lease whose ExpiresAt has passed is
// reaped on the next sweep (OnExpire fires).
func WithPollLeaseTTL(d time.Duration) PollLeaseOption {
	return func(t *PollLeaseTable) {
		if d > 0 {
			t.ttl = d
		}
	}
}

// WithPollLeaseSweepInterval overrides how often the background
// goroutine scans for expired leases. Default 30s. Tests that want
// faster expiry observation should set both this and
// WithPollLeaseTTL to small values.
func WithPollLeaseSweepInterval(d time.Duration) PollLeaseOption {
	return func(t *PollLeaseTable) {
		if d > 0 {
			t.sweepInterval = d
		}
	}
}

// WithPollLeaseOnCreate registers a callback that fires once when a
// (principal, name, params) tuple is observed for the first time
// (lease is newly created). Subsequent polls within the TTL window
// renew the lease without re-firing.
func WithPollLeaseOnCreate(hook PollLeaseHook) PollLeaseOption {
	return func(t *PollLeaseTable) { t.onCreate = hook }
}

// WithPollLeaseOnExpire registers a callback that fires when a lease
// is reaped because its TTL elapsed without a renewing Touch.
func WithPollLeaseOnExpire(hook PollLeaseHook) PollLeaseOption {
	return func(t *PollLeaseTable) { t.onExpire = hook }
}

// pollLease is one entry in the table.
type pollLease struct {
	principal string
	eventName string
	params    map[string]any
	expiresAt time.Time
}

// PollLeaseTable tracks poll-mode subscriptions as ephemeral leases.
// Construct via NewPollLeaseTable. Callers (registerPoll, in events.go)
// invoke Touch on every poll; OnCreate / OnExpire fire from the
// lifecycle of each lease.
//
// All state is in-process — leases do not persist across restart per
// the spec's explicit ephemerality rule (L715). After restart,
// on_subscribe re-fires on the first poll for any active subscription;
// authors should make their on_subscribe idempotent.
type PollLeaseTable struct {
	mu            sync.Mutex
	leases        map[string]*pollLease
	ttl           time.Duration
	sweepInterval time.Duration
	onCreate      PollLeaseHook
	onExpire      PollLeaseHook

	// Lifecycle for the background sweep goroutine. The goroutine is
	// started lazily on the first Touch (so unused tables don't leak)
	// and stopped via Close (test teardown / graceful shutdown).
	// `started` and `closed` are guarded by mu.
	started bool
	closed  bool
	stopCh  chan struct{}
	doneCh  chan struct{}

	// nowFn is overridable for deterministic tests.
	nowFn func() time.Time
}

// NewPollLeaseTable constructs a lease table with sensible defaults
// (5 minute TTL, 30 second sweep interval, no hooks). Override via
// the With* options.
func NewPollLeaseTable(opts ...PollLeaseOption) *PollLeaseTable {
	t := &PollLeaseTable{
		leases:        make(map[string]*pollLease),
		ttl:           defaultPollLeaseTTL,
		sweepInterval: defaultPollLeaseSweepInterval,
		stopCh:        make(chan struct{}),
		doneCh:        make(chan struct{}),
		nowFn:         time.Now,
	}
	for _, o := range opts {
		o(t)
	}
	return t
}

// Touch registers a poll for (principal, eventName, params). Returns
// true when the call newly created the lease (and OnCreate fired for
// it), false when an existing lease was renewed (or the table is
// closed — Close-after-Touch is a no-op).
//
// principal may be empty — anonymous polling on a server without auth
// shares a single lease per (name, params) per spec L707 ("on
// unauthenticated servers all callers share one lease").
func (t *PollLeaseTable) Touch(principal, eventName string, params map[string]any) bool {
	key := pollLeaseKey(principal, eventName, params)
	now := t.nowFn()
	expiresAt := now.Add(t.ttl)

	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return false
	}
	// Lazy-start the sweep goroutine on first use so unused tables
	// (e.g., a freshly registered server nobody polls) don't burn a
	// goroutine. `started` is guarded by mu — we may hold it for
	// strictly longer than the lookup, but the goroutine spawn is
	// cheap and avoids a separate atomic.
	if !t.started {
		t.started = true
		go t.runSweeper()
	}

	existing, ok := t.leases[key]
	if ok {
		existing.expiresAt = expiresAt
		t.mu.Unlock()
		return false
	}
	lease := &pollLease{
		principal: principal,
		eventName: eventName,
		params:    params,
		expiresAt: expiresAt,
	}
	t.leases[key] = lease
	hook := t.onCreate
	t.mu.Unlock()

	if hook != nil {
		invokeLeaseHook("OnCreate", hook, lease)
	}
	return true
}

// Len reports the current number of live leases. Snapshot — callers
// must not race on it for correctness.
func (t *PollLeaseTable) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.leases)
}

// Close stops the background sweep goroutine. Safe to call multiple
// times. After Close, Touch is a no-op (returns false; doesn't track
// new leases or fire OnCreate). Intended for graceful shutdown / test
// teardown.
//
// Channel-safety: stopCh is *closed* (not sent to), so any number of
// readers wake up in lock-step — no risk of a goroutine missing the
// signal. doneCh is closed by the sweeper itself on exit; Close waits
// on it only when started==true, so a never-started table doesn't
// hang at the receive.
func (t *PollLeaseTable) Close() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	started := t.started
	t.mu.Unlock()

	close(t.stopCh)
	if !started {
		// Sweeper never ran — nothing to wait for.
		return
	}
	<-t.doneCh
}

// runSweeper is the body of the background sweep goroutine. Started
// from Touch on first use (under mu, via the started flag) so unused
// tables don't leak a goroutine.
func (t *PollLeaseTable) runSweeper() {
	ticker := time.NewTicker(t.sweepInterval)
	defer ticker.Stop()
	defer close(t.doneCh)
	for {
		select {
		case <-t.stopCh:
			return
		case <-ticker.C:
			t.sweepExpired()
		}
	}
}

// sweepExpired removes leases past their ExpiresAt and fires OnExpire
// for each. Exported-for-test via sweepExpiredForTest. The hook fires
// outside the lock so a long-running OnExpire doesn't serialize Touch.
func (t *PollLeaseTable) sweepExpired() {
	now := t.nowFn()
	t.mu.Lock()
	hook := t.onExpire
	var expired []*pollLease
	for k, l := range t.leases {
		if !l.expiresAt.After(now) {
			expired = append(expired, l)
			delete(t.leases, k)
		}
	}
	t.mu.Unlock()

	if hook == nil {
		return
	}
	for _, l := range expired {
		invokeLeaseHook("OnExpire", hook, l)
	}
}

// sweepExpiredForTest exposes the sweep so tests can drive expiry
// deterministically without waiting for the ticker.
func (t *PollLeaseTable) sweepExpiredForTest() { t.sweepExpired() }

// invokeLeaseHook invokes a PollLeaseHook with panic recovery. A
// hook that panics shouldn't take down the sweep goroutine or the
// poll handler; log + swallow.
func invokeLeaseHook(label string, hook PollLeaseHook, l *pollLease) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[events] poll-lease %s hook panic for (%q,%q): %v",
				label, l.principal, l.eventName, r)
		}
	}()
	hook(l.principal, l.eventName, l.params)
}

// pollLeaseKey returns the canonical-bytes encoding of a
// (principal, eventName, params) tuple — the spec's poll-lease
// identity (L707).
//
// Different shape from identity.go's canonicalKey (which adds
// delivery URL for webhook subscriptions). Same canonical-JSON-of-
// params building block (canonicalJSON) so two polls with map-
// iteration-order-different params hash identically.
//
// Returned as a string for direct use as the lease-table map key.
func pollLeaseKey(principal, eventName string, params map[string]any) string {
	var paramBytes []byte
	if len(params) == 0 {
		paramBytes = []byte("{}")
	} else {
		paramBytes = canonicalJSON(params)
	}
	const sep = "\x1f"
	var b strings.Builder
	b.Grow(len(principal) + len(eventName) + len(paramBytes) + 2)
	b.WriteString(principal)
	b.WriteString(sep)
	b.WriteString(eventName)
	b.WriteString(sep)
	b.Write(paramBytes)
	return b.String()
}
