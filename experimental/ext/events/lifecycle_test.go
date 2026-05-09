package events

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/require"
)

// Lifecycle hook firings across all three delivery modes (η-3).
//
// Acceptance per docs/EVENTS_ETA_PLAN.md:
//   - Hooks fire exactly once per subscription lifecycle in each mode.
//   - Refresh of an existing webhook subscription does NOT re-fire
//     on_subscribe.
//   - TTL expiry of a webhook subscription fires on_unsubscribe.
//   - Suspend transition does NOT fire on_unsubscribe.
//   - Subsequent reactivating-refresh does NOT re-fire on_subscribe.
//   - Poll-lease expiry fires on_unsubscribe.
//   - on_subscribe error rolls back / rejects across all modes.

// hookCounter records on_subscribe / on_unsubscribe firings with the
// HookContext snapshot at fire time so tests can assert mode and
// principal as well as count. Concurrent-safe.
type hookCounter struct {
	mu             sync.Mutex
	subscribes     []hookCall
	unsubscribes   []hookCall
	subscribeErr   error // returned from on_subscribe; nil = succeed
}

type hookCall struct {
	Principal      string
	SubscriptionID string
	Mode           DeliveryMode
	Params         map[string]any
}

func (h *hookCounter) onSubscribe(ctx HookContext, params map[string]any) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.subscribes = append(h.subscribes, hookCall{
		Principal:      ctx.Principal(),
		SubscriptionID: ctx.SubscriptionID(),
		Mode:           ctx.Mode(),
		Params:         params,
	})
	return h.subscribeErr
}

func (h *hookCounter) onUnsubscribe(ctx HookContext, params map[string]any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.unsubscribes = append(h.unsubscribes, hookCall{
		Principal:      ctx.Principal(),
		SubscriptionID: ctx.SubscriptionID(),
		Mode:           ctx.Mode(),
		Params:         params,
	})
}

func (h *hookCounter) subCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subscribes)
}

func (h *hookCounter) unsubCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.unsubscribes)
}

func (h *hookCounter) lastSub() (hookCall, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.subscribes) == 0 {
		return hookCall{}, false
	}
	return h.subscribes[len(h.subscribes)-1], true
}

// lifecycleFixture wraps the boilerplate of registering a single
// YieldingSource with hook fields and standing up an mcp Server with
// init handshake done.
type lifecycleFixture struct {
	t        *testing.T
	srv      *server.Server
	source   *YieldingSource[map[string]any]
	yield    func(map[string]any) error
	def      EventDef
	webhooks *WebhookRegistry
	leases   *PollLeaseTable
	hooks    *hookCounter
}

func newLifecycleFixture(t *testing.T, hooks *hookCounter, webhookOpts ...WebhookOption) *lifecycleFixture {
	t.Helper()
	def := EventDef{
		Name:        "lifecycle.test",
		Description: "lifecycle hook firing tests",
		Delivery:    []string{"poll", "push", "webhook"},
		OnSubscribe: hooks.onSubscribe,
		OnUnsubscribe: hooks.onUnsubscribe,
	}
	src, yield := NewYieldingSource[map[string]any](def)
	wh := NewWebhookRegistry(append([]WebhookOption{
		WithWebhookAllowPrivateNetworks(true),
	}, webhookOpts...)...)
	leases := NewPollLeaseTable(
		WithPollLeaseTTL(40*time.Millisecond),
		WithPollLeaseSweepInterval(time.Hour), // we drive sweeps manually
	)
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	Register(Config{
		Sources:                  []EventSource{src},
		Webhooks:                 wh,
		Server:                   srv,
		PollLeases:               leases,
		UnsafeAnonymousPrincipal: "alice",
		StreamHeartbeatInterval:  500 * time.Millisecond,
	})
	finishInitHandshake(t, srv)
	return &lifecycleFixture{
		t:        t,
		srv:      srv,
		source:   src,
		yield:    yield,
		def:      def,
		webhooks: wh,
		leases:   leases,
		hooks:    hooks,
	}
}

func (f *lifecycleFixture) close() {
	f.leases.Close()
}

// dispatch shells out to the server.Server's Dispatch path with a
// JSON-RPC envelope. Used for events/subscribe / events/unsubscribe.
func (f *lifecycleFixture) dispatch(method string, params map[string]any) *core.Response {
	f.t.Helper()
	raw, err := json.Marshal(params)
	require.NoError(f.t, err)
	resp, err := f.srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  method,
		Params:  raw,
	})
	require.NoError(f.t, err)
	return resp
}

// --- Webhook lifecycle ---

func TestLifecycle_Webhook_SubscribeUnsubscribe_FiresHooksOnce(t *testing.T) {
	hooks := &hookCounter{}
	f := newLifecycleFixture(t, hooks)
	defer f.close()

	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	subParams := map[string]any{
		"name":   "lifecycle.test",
		"params": map[string]any{"sev": "high"},
		"delivery": map[string]any{
			"mode":   "webhook",
			"url":    receiver.URL,
			"secret": "whsec_" + strings.Repeat("a", 32),
		},
	}
	resp := f.dispatch("events/subscribe", subParams)
	require.Nil(t, resp.Error, "subscribe failed: %+v", resp.Error)
	if hooks.subCount() != 1 {
		t.Fatalf("after subscribe: subscribes=%d, want 1", hooks.subCount())
	}
	if hooks.unsubCount() != 0 {
		t.Fatalf("after subscribe: unsubscribes=%d, want 0", hooks.unsubCount())
	}
	last, _ := hooks.lastSub()
	if last.Mode != DeliveryModeWebhook {
		t.Errorf("on_subscribe Mode = %v, want DeliveryModeWebhook", last.Mode)
	}
	if last.Principal != "alice" {
		t.Errorf("on_subscribe Principal = %q, want \"alice\"", last.Principal)
	}
	if last.SubscriptionID == "" || !strings.HasPrefix(last.SubscriptionID, "sub_") {
		t.Errorf("on_subscribe SubscriptionID = %q, want non-empty sub_ prefixed", last.SubscriptionID)
	}

	unsubParams := map[string]any{
		"name":     "lifecycle.test",
		"params":   map[string]any{"sev": "high"},
		"delivery": map[string]any{"url": receiver.URL},
	}
	resp = f.dispatch("events/unsubscribe", unsubParams)
	require.Nil(t, resp.Error, "unsubscribe failed: %+v", resp.Error)
	if hooks.unsubCount() != 1 {
		t.Fatalf("after unsubscribe: unsubscribes=%d, want 1", hooks.unsubCount())
	}
	if hooks.subCount() != 1 {
		t.Fatalf("after unsubscribe: subscribes=%d (would have re-fired); want 1", hooks.subCount())
	}
}

func TestLifecycle_Webhook_RefreshDoesNotReFireOnSubscribe(t *testing.T) {
	hooks := &hookCounter{}
	f := newLifecycleFixture(t, hooks)
	defer f.close()

	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	subParams := map[string]any{
		"name":   "lifecycle.test",
		"params": map[string]any{"sev": "high"},
		"delivery": map[string]any{
			"mode":   "webhook",
			"url":    receiver.URL,
			"secret": "whsec_" + strings.Repeat("a", 32),
		},
	}
	for i := 0; i < 3; i++ {
		resp := f.dispatch("events/subscribe", subParams)
		require.Nil(t, resp.Error, "subscribe %d failed: %+v", i, resp.Error)
	}
	if hooks.subCount() != 1 {
		t.Fatalf("after 3 subscribes (1 new + 2 refresh): subscribes=%d, want 1", hooks.subCount())
	}
	if hooks.unsubCount() != 0 {
		t.Fatalf("refresh fired unsubscribe: %d, want 0", hooks.unsubCount())
	}
}

func TestLifecycle_Webhook_TTLPruneFiresOnUnsubscribe(t *testing.T) {
	hooks := &hookCounter{}
	// Set a long TTL on the registry but use ExpireAll to force the
	// next Register to prune. (The pruning side effect is what fires
	// the unsubscribe hook.)
	f := newLifecycleFixture(t, hooks, WithWebhookTTL(time.Hour))
	defer f.close()

	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	subParams := map[string]any{
		"name":   "lifecycle.test",
		"params": map[string]any{"sev": "high"},
		"delivery": map[string]any{
			"mode":   "webhook",
			"url":    receiver.URL,
			"secret": "whsec_" + strings.Repeat("a", 32),
		},
	}
	resp := f.dispatch("events/subscribe", subParams)
	require.Nil(t, resp.Error)

	// Force expiry, then a Register call to trigger pruneExpiredLocked.
	f.webhooks.ExpireAll()
	resp = f.dispatch("events/subscribe", map[string]any{
		"name": "lifecycle.test",
		"params": map[string]any{"sev": "low"}, // distinct → new sub, triggers prune
		"delivery": map[string]any{
			"mode":   "webhook",
			"url":    receiver.URL,
			"secret": "whsec_" + strings.Repeat("b", 32),
		},
	})
	require.Nil(t, resp.Error)

	if hooks.unsubCount() != 1 {
		t.Fatalf("after TTL prune: unsubscribes=%d, want 1 (the expired sub)", hooks.unsubCount())
	}
}

func TestLifecycle_Webhook_PostTerminatedFiresOnUnsubscribe(t *testing.T) {
	hooks := &hookCounter{}
	f := newLifecycleFixture(t, hooks)
	defer f.close()

	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	subParams := map[string]any{
		"name":   "lifecycle.test",
		"params": map[string]any{"sev": "high"},
		"delivery": map[string]any{
			"mode":   "webhook",
			"url":    receiver.URL,
			"secret": "whsec_" + strings.Repeat("a", 32),
		},
	}
	resp := f.dispatch("events/subscribe", subParams)
	require.Nil(t, resp.Error)

	// PostTerminated drives the server-initiated death path. Look up
	// the canonical key the registry stored.
	targets := f.webhooks.Targets()
	require.Len(t, targets, 1, "expected exactly one registered target")
	f.webhooks.PostTerminated(targets[0].CanonicalKey, ControlError{Code: -32603, Message: "test"})

	if hooks.unsubCount() != 1 {
		t.Fatalf("PostTerminated: unsubscribes=%d, want 1", hooks.unsubCount())
	}
}

// TestLifecycle_Webhook_SuspendDoesNotFireOnUnsubscribe verifies that
// the suspend transition (Active=true→false from repeated delivery
// failures, ζ-6) does NOT fire on_unsubscribe — the subscription is
// paused, not removed; refresh reactivates without re-firing
// on_subscribe (Q4).
func TestLifecycle_Webhook_SuspendDoesNotFireOnUnsubscribe(t *testing.T) {
	hooks := &hookCounter{}
	const threshold = 2
	failingReceiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failingReceiver.Close()

	f := newLifecycleFixture(t, hooks,
		WithWebhookSuspendThreshold(threshold),
		WithWebhookSuspendWindow(10*time.Second),
	)
	defer f.close()

	subParams := map[string]any{
		"name":   "lifecycle.test",
		"params": map[string]any{"sev": "high"},
		"delivery": map[string]any{
			"mode":   "webhook",
			"url":    failingReceiver.URL,
			"secret": "whsec_" + strings.Repeat("a", 32),
		},
	}
	resp := f.dispatch("events/subscribe", subParams)
	require.Nil(t, resp.Error)
	require.Equal(t, 1, hooks.subCount())

	// Drive `threshold` consecutive failed deliveries to trigger
	// suspend. recordDeliveryFailure flips Active=false and calls
	// postTerminatedSilent (not PostTerminated) — no registry
	// deletion → onRemove must not fire.
	target := f.webhooks.Targets()[0]
	for i := 0; i < threshold; i++ {
		f.webhooks.deliver(target, "evt_"+string(rune('a'+i)), []byte(`{}`))
	}

	st := f.webhooks.DeliveryStatus(target.CanonicalKey)
	require.False(t, st.Active, "target should be suspended; got %+v", st)
	if hooks.unsubCount() != 0 {
		t.Errorf("suspend transition fired %d on_unsubscribe; want 0 (suspend ≠ unsubscribe)",
			hooks.unsubCount())
	}

	// Reactivating refresh: same canonical tuple. Active flips back
	// to true; on_subscribe MUST NOT re-fire.
	resp = f.dispatch("events/subscribe", subParams)
	require.Nil(t, resp.Error)
	st = f.webhooks.DeliveryStatus(target.CanonicalKey)
	require.True(t, st.Active, "refresh should reactivate suspended target")
	if hooks.subCount() != 1 {
		t.Errorf("reactivating refresh re-fired on_subscribe (now %d); want 1", hooks.subCount())
	}
}

// TestLifecycle_Webhook_OnSubscribeError_RollsBack verifies that an
// author returning error from on_subscribe rolls the registration
// back so a rejected subscription doesn't sit in the registry as
// half-provisioned.
func TestLifecycle_Webhook_OnSubscribeError_RollsBack(t *testing.T) {
	hooks := &hookCounter{subscribeErr: errStub("upstream quota exceeded")}
	f := newLifecycleFixture(t, hooks)
	defer f.close()

	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	resp := f.dispatch("events/subscribe", map[string]any{
		"name":   "lifecycle.test",
		"params": map[string]any{"sev": "high"},
		"delivery": map[string]any{
			"mode":   "webhook",
			"url":    receiver.URL,
			"secret": "whsec_" + strings.Repeat("a", 32),
		},
	})
	require.NotNil(t, resp.Error, "on_subscribe error should produce a JSON-RPC error response")
	require.Equal(t, ErrCodeTooManySubscriptions, resp.Error.Code)

	// Rollback assertion: registry should not retain the rejected sub.
	if got := len(f.webhooks.Targets()); got != 0 {
		t.Errorf("rollback failed: registry contains %d targets after on_subscribe error; want 0", got)
	}
	// Rollback fires onRemove (Unregister), which would call
	// on_unsubscribe — that's fine; the author's hook is allowed to
	// see the teardown of the subscription it just rejected.
	if hooks.subCount() != 1 {
		t.Errorf("on_subscribe call count = %d; want 1", hooks.subCount())
	}
}

// --- Push (events/stream) lifecycle ---

func TestLifecycle_Push_OpenCloseFiresHooksOnce(t *testing.T) {
	hooks := &hookCounter{}
	f := newLifecycleFixture(t, hooks)
	defer f.close()

	// events/stream blocks until ctx cancels. Run it in a goroutine
	// with a short cancel so we can observe both subscribe (after
	// the channel is acquired) and unsubscribe (on return).
	ctx, cancel := context.WithCancel(context.Background())
	rawReq, err := json.Marshal(map[string]any{
		"name":   "lifecycle.test",
		"params": map[string]any{"sev": "high"},
	})
	require.NoError(t, err)

	streamDone := make(chan struct{})
	go func() {
		defer close(streamDone)
		_, _ = f.srv.Dispatch(ctx, &core.Request{
			JSONRPC: "2.0",
			ID:      json.RawMessage(`2`),
			Method:  "events/stream",
			Params:  rawReq,
		})
	}()

	// Wait for on_subscribe to fire (stream opened).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && hooks.subCount() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if hooks.subCount() != 1 {
		t.Fatalf("after stream open: subscribes=%d, want 1", hooks.subCount())
	}
	last, _ := hooks.lastSub()
	if last.Mode != DeliveryModePush {
		t.Errorf("push on_subscribe Mode = %v, want DeliveryModePush", last.Mode)
	}
	if !strings.HasPrefix(last.SubscriptionID, "sub_") {
		t.Errorf("push on_subscribe SubscriptionID = %q, want sub_-prefixed", last.SubscriptionID)
	}

	cancel()
	select {
	case <-streamDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("stream did not return after ctx cancel")
	}

	if hooks.unsubCount() != 1 {
		t.Fatalf("after stream close: unsubscribes=%d, want 1", hooks.unsubCount())
	}
}

func TestLifecycle_Push_OnSubscribeError_RejectsStream(t *testing.T) {
	hooks := &hookCounter{subscribeErr: errStub("upstream busy")}
	f := newLifecycleFixture(t, hooks)
	defer f.close()

	rawReq, err := json.Marshal(map[string]any{
		"name":   "lifecycle.test",
		"params": map[string]any{"sev": "high"},
	})
	require.NoError(t, err)
	resp, err := f.srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`3`),
		Method:  "events/stream",
		Params:  rawReq,
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Error)
	require.Equal(t, ErrCodeTooManySubscriptions, resp.Error.Code)
	// on_unsubscribe deliberately does NOT fire — we never crossed
	// the live-subscription line. (defer is set up after the
	// safeOnSubscribe error check.)
	if hooks.unsubCount() != 0 {
		t.Errorf("rejected stream fired %d on_unsubscribe; want 0", hooks.unsubCount())
	}
}

// --- Poll lifecycle ---

func TestLifecycle_Poll_FirstPollFiresOnSubscribe(t *testing.T) {
	hooks := &hookCounter{}
	f := newLifecycleFixture(t, hooks)
	defer f.close()

	dispatchPoll(t, f.srv, "lifecycle.test", map[string]any{"sev": "high"})
	if hooks.subCount() != 1 {
		t.Fatalf("first poll: subscribes=%d, want 1", hooks.subCount())
	}
	last, _ := hooks.lastSub()
	if last.Mode != DeliveryModePoll {
		t.Errorf("poll on_subscribe Mode = %v, want DeliveryModePoll", last.Mode)
	}
	// Poll has no sub id per Q4.
	if last.SubscriptionID != "" {
		t.Errorf("poll on_subscribe SubscriptionID = %q, want \"\" (Q4: poll has no sub id)", last.SubscriptionID)
	}
}

func TestLifecycle_Poll_RepeatedPollDoesNotReFire(t *testing.T) {
	hooks := &hookCounter{}
	f := newLifecycleFixture(t, hooks)
	defer f.close()

	for i := 0; i < 5; i++ {
		dispatchPoll(t, f.srv, "lifecycle.test", map[string]any{"sev": "high"})
	}
	if hooks.subCount() != 1 {
		t.Fatalf("after 5 polls: subscribes=%d, want 1", hooks.subCount())
	}
}

func TestLifecycle_Poll_TTLExpiryFiresOnUnsubscribe(t *testing.T) {
	hooks := &hookCounter{}
	f := newLifecycleFixture(t, hooks)
	defer f.close()

	dispatchPoll(t, f.srv, "lifecycle.test", map[string]any{"sev": "high"})
	require.Equal(t, 1, hooks.subCount())

	// Wait past TTL (40ms in fixture), then drive the sweep.
	time.Sleep(60 * time.Millisecond)
	f.leases.sweepExpiredForTest()

	// Sweep fires asynchronously through chainOnExpire; give it a
	// moment to deliver. (sweepExpiredForTest fires hooks
	// synchronously in the calling goroutine, but the chained hook
	// invokes safeOnUnsubscribe which is also sync; just be safe.)
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) && hooks.unsubCount() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if hooks.unsubCount() != 1 {
		t.Fatalf("poll TTL expiry: unsubscribes=%d, want 1", hooks.unsubCount())
	}
}

func TestLifecycle_Poll_OnSubscribeError_Rejects(t *testing.T) {
	hooks := &hookCounter{subscribeErr: errStub("poll quota")}
	f := newLifecycleFixture(t, hooks)
	defer f.close()

	body := map[string]any{
		"name":   "lifecycle.test",
		"params": map[string]any{"sev": "high"},
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	resp, err := f.srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`4`),
		Method:  "events/poll",
		Params:  raw,
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Error, "poll on_subscribe error should surface via JSON-RPC error")
	require.Equal(t, ErrCodeTooManySubscriptions, resp.Error.Code)
}

// --- Concurrency safety ---

// Concurrent subscribes for the same canonical tuple should fire
// on_subscribe exactly once (the second call is a refresh — the
// registry's isNew flag determines this atomically under the lock).
func TestLifecycle_Webhook_ConcurrentSubscribe_FiresOnce(t *testing.T) {
	hooks := &hookCounter{}
	f := newLifecycleFixture(t, hooks)
	defer f.close()

	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	subParams := map[string]any{
		"name":   "lifecycle.test",
		"params": map[string]any{"sev": "high"},
		"delivery": map[string]any{
			"mode":   "webhook",
			"url":    receiver.URL,
			"secret": "whsec_" + strings.Repeat("a", 32),
		},
	}

	raw, err := json.Marshal(subParams)
	require.NoError(t, err)

	const goroutines = 8
	var wg sync.WaitGroup
	var fails atomic.Int32
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		i := i
		go func() {
			defer wg.Done()
			// Distinct IDs so the dispatcher doesn't dedup
			// concurrent identical envelopes.
			idRaw := json.RawMessage([]byte{'"', 'r', byte('0' + i), '"'})
			resp, derr := f.srv.Dispatch(context.Background(), &core.Request{
				JSONRPC: "2.0",
				ID:      idRaw,
				Method:  "events/subscribe",
				Params:  raw,
			})
			if derr != nil || (resp != nil && resp.Error != nil) {
				fails.Add(1)
				if resp != nil && resp.Error != nil {
					t.Logf("subscribe goroutine %d error: %+v", i, resp.Error)
				}
			}
		}()
	}
	wg.Wait()
	if fails.Load() != 0 {
		t.Fatalf("concurrent subscribes had %d failures; want 0", fails.Load())
	}
	if hooks.subCount() != 1 {
		t.Errorf("concurrent subscribes for same canonical tuple fired on_subscribe %d times; want 1", hooks.subCount())
	}
}

// errStub returns a sentinel error for the on_subscribe rejection
// tests. Inline so the lifecycle tests don't import a heavy errors
// package for one-off sentinels.
type errStub string

func (e errStub) Error() string { return string(e) }
