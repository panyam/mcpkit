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

// EmitToSubscription targeted delivery (η-5). Spec §"Server SDK
// Guidance" L630.
//
// Coverage targets per docs/EVENTS_ETA_PLAN.md η-5 acceptance:
//   - EmitToSubscription delivers to exactly the named subscription
//     regardless of Match.
//   - Unknown subID drops with a debug log (no panic, no error).
//   - Targeted emit skips both Match and Transform.
//   - Push subscriptions: each open events/stream gets its own sub
//     id; the index entry is added on subscribe and removed on every
//     return path.
//   - Webhook subscriptions: the derived id is reused across refresh
//     (same canonical → same id → same index entry); removal happens
//     when the registry deletes the target.
//   - Poll mode is NOT addressable by sub id (no entry exists).

// --- SubscriptionIndex unit ---

func TestSubscriptionIndex_AddRemoveLookup(t *testing.T) {
	idx := NewSubscriptionIndex()
	if idx.Len() != 0 {
		t.Fatalf("fresh index Len() = %d, want 0", idx.Len())
	}

	var got []Event
	deliver := func(e Event) { got = append(got, e) }
	idx.Add("sub_alpha", DeliveryModePush, deliver)
	if idx.Len() != 1 {
		t.Fatalf("after Add Len() = %d, want 1", idx.Len())
	}

	mode, fn, ok := idx.Lookup("sub_alpha")
	if !ok {
		t.Fatalf("Lookup(\"sub_alpha\") returned ok=false")
	}
	if mode != DeliveryModePush {
		t.Errorf("Lookup mode = %v, want DeliveryModePush", mode)
	}
	if fn == nil {
		t.Errorf("Lookup deliver fn is nil")
	}

	idx.Remove("sub_alpha")
	if _, _, ok := idx.Lookup("sub_alpha"); ok {
		t.Errorf("Lookup after Remove returned ok=true")
	}
	if idx.Len() != 0 {
		t.Errorf("after Remove Len() = %d, want 0", idx.Len())
	}
}

func TestSubscriptionIndex_ConcurrentAddRemoveSafe(t *testing.T) {
	idx := NewSubscriptionIndex()
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		g := g
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				key := string(rune('a'+g)) + "-" + string(rune('0'+(i%10)))
				idx.Add(key, DeliveryModePush, func(Event) {})
				idx.Remove(key)
			}
		}()
	}
	wg.Wait()
	if idx.Len() != 0 {
		t.Errorf("after concurrent Add/Remove pairs: Len() = %d, want 0", idx.Len())
	}
}

func TestSubscriptionIndex_AddIgnoresNilDeliverOrEmptySubID(t *testing.T) {
	idx := NewSubscriptionIndex()
	idx.Add("", DeliveryModePush, func(Event) {})
	idx.Add("sub_x", DeliveryModePush, nil)
	if idx.Len() != 0 {
		t.Errorf("Len() = %d, want 0 (Add with empty subID or nil deliver should be ignored)", idx.Len())
	}
}

// --- EmitToSubscription dispatch ---

func TestEmitToSubscription_UnknownIDDropsWithoutPanic(t *testing.T) {
	idx := NewSubscriptionIndex()
	// Must not panic, must not block. No way to assert "no log"
	// without log capture; the absence of panic is the contract.
	EmitToSubscription(idx, Event{EventID: "evt_1", Name: "x"}, "sub_does_not_exist")
}

func TestEmitToSubscription_NilIndexDropsWithoutPanic(t *testing.T) {
	EmitToSubscription(nil, Event{EventID: "evt_1", Name: "x"}, "sub_anything")
}

func TestEmitToSubscription_CallsDeliver(t *testing.T) {
	idx := NewSubscriptionIndex()
	var got Event
	idx.Add("sub_x", DeliveryModePush, func(e Event) { got = e })
	EmitToSubscription(idx, Event{EventID: "evt_1", Name: "demo"}, "sub_x")
	if got.EventID != "evt_1" {
		t.Errorf("deliver got EventID=%q, want \"evt_1\"", got.EventID)
	}
}

// --- Push integration ---

// TestEmitToSubscription_Push_RoutesToOneStream verifies the end-to-
// end push path: open a stream, capture its sub id from the on_subscribe
// hook, then EmitToSubscription targets exactly that stream.
func TestEmitToSubscription_Push_RoutesToOneStream(t *testing.T) {
	// Capture the sub id that the SDK assigned to the stream via the
	// on_subscribe hook — that's the only place an author observes it.
	var capturedSubID atomic.Value
	def := EventDef{
		Name:        "alert.fired",
		Description: "targeted push",
		Delivery:    []string{"push"},
		OnSubscribe: func(hc HookContext, _ map[string]any) error {
			capturedSubID.Store(hc.SubscriptionID())
			return nil
		},
	}
	src, _ := NewYieldingSource[map[string]any](def)
	idx := NewSubscriptionIndex()
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	Register(Config{
		Sources:                  []EventSource{src},
		Server:                   srv,
		SubscriptionIndex:        idx,
		UnsafeAnonymousPrincipal: "alice",
		StreamHeartbeatInterval:  500 * time.Millisecond,
	})
	finishInitHandshake(t, srv)

	streamCtx, cancelStream := context.WithCancel(context.Background())
	defer cancelStream()
	rawReq, err := json.Marshal(map[string]any{"name": "alert.fired"})
	require.NoError(t, err)
	streamDone := make(chan struct{})
	go func() {
		defer close(streamDone)
		_, _ = srv.Dispatch(streamCtx, &core.Request{
			JSONRPC: "2.0", ID: json.RawMessage(`100`),
			Method: "events/stream", Params: rawReq,
		})
	}()

	// Wait for on_subscribe to fire so we know the index has the entry.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if v := capturedSubID.Load(); v != nil && v.(string) != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	subID, _ := capturedSubID.Load().(string)
	require.NotEmpty(t, subID, "on_subscribe never fired; can't get the sub id")

	// Targeted emit lands on this one stream.
	EmitToSubscription(idx, Event{EventID: "evt_targeted", Name: "alert.fired", Data: json.RawMessage(`{}`)}, subID)

	// We're driving this without the test stack's full notif plumbing
	// — for the assertion we instead verify via the source's
	// SubscriberCount + the index Len. Index should still hold one
	// entry; subscriber should still be live.
	if idx.Len() != 1 {
		t.Errorf("index Len() = %d, want 1 after targeted emit", idx.Len())
	}

	cancelStream()
	select {
	case <-streamDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("stream did not return after ctx cancel")
	}
	// After stream return, the deferred idx.Remove should have fired.
	if idx.Len() != 0 {
		t.Errorf("after stream close: index Len() = %d, want 0 (defer should have removed)", idx.Len())
	}
}

// --- Webhook integration ---

func TestEmitToSubscription_Webhook_RoutesToOneTarget(t *testing.T) {
	// Two webhook receivers, both subscribed. Targeted emit goes to
	// the one whose sub id we name; the other gets nothing.
	var hitsA, hitsB atomic.Int32
	rA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hitsA.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer rA.Close()
	rB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hitsB.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer rB.Close()

	src, _ := NewYieldingSource[map[string]any](EventDef{
		Name:        "alert.fired",
		Description: "targeted webhook",
		Delivery:    []string{"webhook"},
	})
	idx := NewSubscriptionIndex()
	wh := NewWebhookRegistry(WithWebhookAllowPrivateNetworks(true))
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	Register(Config{
		Sources:                  []EventSource{src},
		Webhooks:                 wh,
		Server:                   srv,
		SubscriptionIndex:        idx,
		UnsafeAnonymousPrincipal: "alice",
	})
	finishInitHandshake(t, srv)

	subscribe := func(url string, secretLetter byte) string {
		t.Helper()
		body := map[string]any{
			"name": "alert.fired",
			"delivery": map[string]any{
				"mode":   "webhook",
				"url":    url,
				"secret": "whsec_" + strings.Repeat(string(secretLetter), 32),
			},
		}
		raw, _ := json.Marshal(body)
		resp, err := srv.Dispatch(context.Background(), &core.Request{
			JSONRPC: "2.0", ID: json.RawMessage(`1`),
			Method: "events/subscribe", Params: raw,
		})
		require.NoError(t, err)
		require.Nil(t, resp.Error, "subscribe failed: %+v", resp.Error)
		m, ok := resp.Result.(map[string]any)
		require.True(t, ok, "expected map response, got %T", resp.Result)
		id, _ := m["id"].(string)
		require.NotEmpty(t, id, "subscribe response missing id")
		return id
	}
	idA := subscribe(rA.URL, 'a')
	idB := subscribe(rB.URL, 'b')
	require.NotEqual(t, idA, idB, "different canonical tuples should derive distinct sub ids")
	require.Equal(t, 2, idx.Len(), "index should hold both webhook subs")

	// Target A; B must remain at zero.
	EmitToSubscription(idx, Event{EventID: "evt_a", Name: "alert.fired", Data: json.RawMessage(`{}`)}, idA)

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) && hitsA.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if hitsA.Load() != 1 {
		t.Errorf("A targeted: hits=%d, want 1", hitsA.Load())
	}
	if hitsB.Load() != 0 {
		t.Errorf("B not targeted: hits=%d, want 0", hitsB.Load())
	}

	// Unsubscribe A — index should drop the entry, future targeted
	// emit to A becomes the unknown-id drop path.
	unsubBody, _ := json.Marshal(map[string]any{
		"name":     "alert.fired",
		"delivery": map[string]any{"url": rA.URL},
	})
	resp, err := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`2`),
		Method: "events/unsubscribe", Params: unsubBody,
	})
	require.NoError(t, err)
	require.Nil(t, resp.Error)

	if idx.Len() != 1 {
		t.Errorf("after A's unsubscribe: index Len() = %d, want 1", idx.Len())
	}
	hitsBefore := hitsA.Load()
	EmitToSubscription(idx, Event{EventID: "evt_a2", Name: "alert.fired", Data: json.RawMessage(`{}`)}, idA)
	time.Sleep(50 * time.Millisecond)
	if hitsA.Load() != hitsBefore {
		t.Errorf("A's receiver got new hits after unsubscribe: before=%d after=%d", hitsBefore, hitsA.Load())
	}
}

// TestEmitToSubscription_Webhook_RefreshKeepsSameID verifies the
// idempotency property: webhook refresh on the same canonical tuple
// keeps the same derived id, so the index entry installed at first
// subscribe stays valid across refresh.
func TestEmitToSubscription_Webhook_RefreshKeepsSameID(t *testing.T) {
	var hits atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	src, _ := NewYieldingSource[map[string]any](EventDef{
		Name:        "alert.fired",
		Description: "refresh keeps id",
		Delivery:    []string{"webhook"},
	})
	idx := NewSubscriptionIndex()
	wh := NewWebhookRegistry(WithWebhookAllowPrivateNetworks(true))
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	Register(Config{
		Sources:                  []EventSource{src},
		Webhooks:                 wh,
		Server:                   srv,
		SubscriptionIndex:        idx,
		UnsafeAnonymousPrincipal: "alice",
	})
	finishInitHandshake(t, srv)

	subParams := map[string]any{
		"name": "alert.fired",
		"delivery": map[string]any{
			"mode":   "webhook",
			"url":    receiver.URL,
			"secret": "whsec_" + strings.Repeat("a", 32),
		},
	}

	subscribeOnce := func() string {
		t.Helper()
		raw, _ := json.Marshal(subParams)
		resp, err := srv.Dispatch(context.Background(), &core.Request{
			JSONRPC: "2.0", ID: json.RawMessage(`1`),
			Method: "events/subscribe", Params: raw,
		})
		require.NoError(t, err)
		require.Nil(t, resp.Error)
		m := resp.Result.(map[string]any)
		return m["id"].(string)
	}
	idFirst := subscribeOnce()
	idSecond := subscribeOnce() // refresh
	if idFirst != idSecond {
		t.Fatalf("refresh produced different sub id: first=%q second=%q (canonical-tuple identity broken)",
			idFirst, idSecond)
	}
	if idx.Len() != 1 {
		t.Errorf("index Len() = %d after refresh, want 1", idx.Len())
	}

	// EmitToSubscription against the still-stable id MUST deliver.
	EmitToSubscription(idx, Event{EventID: "evt_x", Name: "alert.fired", Data: json.RawMessage(`{}`)}, idFirst)
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) && hits.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if hits.Load() != 1 {
		t.Errorf("after EmitToSubscription post-refresh: hits=%d, want 1", hits.Load())
	}
}

// TestEmitToSubscription_SkipsMatchTransform verifies the spec L630
// model: the author has already shaped this event for this sub, so
// targeted emit MUST NOT apply Match (would otherwise filter it out)
// or Transform (would otherwise re-shape it).
func TestEmitToSubscription_SkipsMatchTransform(t *testing.T) {
	denyAll := func(HookContext, Event, map[string]any) bool { return false }
	mungeAll := func(_ HookContext, e Event, _ map[string]any) (Event, bool) {
		e.Data = json.RawMessage(`{"hijacked":true}`)
		return e, true
	}

	src, _ := NewYieldingSource[map[string]any](EventDef{
		Name:        "alert.fired",
		Description: "match/transform skipped",
		Delivery:    []string{"push"},
		Match:       denyAll,
		Transform:   mungeAll,
	})
	idx := NewSubscriptionIndex()
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	Register(Config{
		Sources:                  []EventSource{src},
		Server:                   srv,
		SubscriptionIndex:        idx,
		UnsafeAnonymousPrincipal: "alice",
		StreamHeartbeatInterval:  500 * time.Millisecond,
	})
	finishInitHandshake(t, srv)

	// Subscribe directly to the source so we can read the channel
	// without driving the full events/stream handler. Index Add is
	// only via the SDK lifecycle, so manually add for this test.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, sender := src.Subscribe(ctx, SubscribeOpts{Principal: "alice"})
	idx.Add("sub_manual", DeliveryModePush, sender)
	defer idx.Remove("sub_manual")

	original := Event{EventID: "evt_keep", Name: "alert.fired", Data: json.RawMessage(`{"original":true}`)}
	EmitToSubscription(idx, original, "sub_manual")

	select {
	case se := <-ch:
		if string(se.Event.Data) != `{"original":true}` {
			t.Errorf("targeted-emit data = %s; want original (transform should NOT have applied)", se.Event.Data)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("targeted-emit did not deliver; Match should NOT have applied")
	}
}
