package events

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TooManySubscriptions enforcement per spec §"Server SDK Guidance" →
// "Subscription lifecycle hooks" L705. Acceptance:
//   - Cap of N per-event-type per-principal: (N+1)th subscribe
//     returns -32013.
//   - Same cap applies to push and poll modes consistently.
//   - Unsubscribe / TTL expiry / lease-expire releases the budget.
//   - on_subscribe fires only AFTER quota check passes.
//   - -32013 payload includes a human-readable message naming the
//     event-type and the cap.
//   - Cross-event-type and cross-principal counts are isolated.

// --- Quota unit ---

func TestQuota_NoCap_AlwaysSucceeds(t *testing.T) {
	q := NewQuota()
	for i := 0; i < 100; i++ {
		if err := q.Reserve("alice", "no.cap"); err != nil {
			t.Fatalf("Reserve %d: got %v, want nil (no cap configured)", i, err)
		}
	}
}

func TestQuota_CapEnforced(t *testing.T) {
	q := NewQuota(WithMaxSubscriptionsPerPrincipal("alert.fired", 2))
	require.NoError(t, q.Reserve("alice", "alert.fired"))
	require.NoError(t, q.Reserve("alice", "alert.fired"))
	err := q.Reserve("alice", "alert.fired")
	if err == nil {
		t.Fatal("third Reserve should fail; got nil")
	}
	if !errors.Is(err, ErrTooManySubscriptions) {
		t.Errorf("error not ErrTooManySubscriptions: %v", err)
	}
	msg := err.Error()
	for _, want := range []string{`"alice"`, `"alert.fired"`, "cap 2"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q; full message: %q", want, msg)
		}
	}
}

func TestQuota_ReleaseRestoresSlot(t *testing.T) {
	q := NewQuota(WithMaxSubscriptionsPerPrincipal("alert.fired", 2))
	require.NoError(t, q.Reserve("alice", "alert.fired"))
	require.NoError(t, q.Reserve("alice", "alert.fired"))
	if err := q.Reserve("alice", "alert.fired"); err == nil {
		t.Fatal("third Reserve should fail")
	}
	q.Release("alice", "alert.fired")
	if err := q.Reserve("alice", "alert.fired"); err != nil {
		t.Errorf("Reserve after Release: got %v, want nil", err)
	}
}

func TestQuota_CrossPrincipalIsolated(t *testing.T) {
	q := NewQuota(WithMaxSubscriptionsPerPrincipal("alert.fired", 1))
	require.NoError(t, q.Reserve("alice", "alert.fired"))
	if err := q.Reserve("bob", "alert.fired"); err != nil {
		t.Errorf("bob's Reserve blocked by alice's count: %v", err)
	}
}

func TestQuota_CrossEventTypeIsolated(t *testing.T) {
	q := NewQuota(
		WithMaxSubscriptionsPerPrincipal("alert.fired", 1),
		WithMaxSubscriptionsPerPrincipal("alert.cleared", 1),
	)
	require.NoError(t, q.Reserve("alice", "alert.fired"))
	if err := q.Reserve("alice", "alert.cleared"); err != nil {
		t.Errorf("alice's Reserve on alert.cleared blocked by alert.fired count: %v", err)
	}
}

func TestQuota_ReleaseWithoutCapIsNoop(t *testing.T) {
	q := NewQuota()
	q.Release("alice", "no.cap") // must not panic
}

func TestQuota_ReleaseUnknownIsNoop(t *testing.T) {
	q := NewQuota(WithMaxSubscriptionsPerPrincipal("x", 5))
	q.Release("never-reserved", "x") // must not panic
}

// --- Webhook integration ---

func TestQuota_Webhook_RejectsThirdSubscribe(t *testing.T) {
	src, _ := NewYieldingSource[map[string]any](EventDef{
		Name:     "alert.fired",
		Delivery: []string{"webhook"},
	})
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer receiver.Close()

	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	wh := NewWebhookRegistry(WithWebhookAllowPrivateNetworks(true))
	q := NewQuota(WithMaxSubscriptionsPerPrincipal("alert.fired", 2))
	Register(Config{
		Sources:                  []EventSource{src},
		Webhooks:                 wh,
		Server:                   srv,
		Quota:                    q,
		UnsafeAnonymousPrincipal: "alice",
	})
	finishInitHandshake(t, srv)

	subscribe := func(t *testing.T, urlVariant string) *core.Response {
		t.Helper()
		body := map[string]any{
			"name":   "alert.fired",
			"arguments": map[string]any{"variant": urlVariant},
			"delivery": map[string]any{
				"mode":   "webhook",
				"url":    receiver.URL + "/" + urlVariant,
				"secret": "whsec_" + strings.Repeat("a", 32),
			},
		}
		raw, _ := json.Marshal(body)
		resp, err := srv.Dispatch(context.Background(), &core.Request{
			JSONRPC: "2.0", ID: json.RawMessage(`1`),
			Method: "events/subscribe", Params: raw,
		})
		require.NoError(t, err)
		return resp
	}

	require.Nil(t, subscribe(t, "a").Error)
	require.Nil(t, subscribe(t, "b").Error)
	resp := subscribe(t, "c")
	require.NotNil(t, resp.Error, "third subscribe should be rejected")
	require.Equal(t, ErrCodeResourceExhausted, resp.Error.Code)
	require.Contains(t, resp.Error.Message, "alert.fired")
	require.Contains(t, resp.Error.Message, "cap 2")
	// Typed data payload: clients branch on (limit, max) without
	// parsing the human-readable message.
	dataBytes, err := json.Marshal(resp.Error.Data)
	require.NoError(t, err)
	assert.JSONEq(t, `{"limit":"subscriptions","max":2}`, string(dataBytes),
		"quota Reserve failure must carry typed ResourceExhaustedData with the configured cap")
}

func TestQuota_Webhook_UnsubscribeReleases(t *testing.T) {
	src, _ := NewYieldingSource[map[string]any](EventDef{
		Name:     "alert.fired",
		Delivery: []string{"webhook"},
	})
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer receiver.Close()

	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	wh := NewWebhookRegistry(WithWebhookAllowPrivateNetworks(true))
	q := NewQuota(WithMaxSubscriptionsPerPrincipal("alert.fired", 1))
	Register(Config{
		Sources:                  []EventSource{src},
		Webhooks:                 wh,
		Server:                   srv,
		Quota:                    q,
		UnsafeAnonymousPrincipal: "alice",
	})
	finishInitHandshake(t, srv)

	subBody, _ := json.Marshal(map[string]any{
		"name": "alert.fired",
		"delivery": map[string]any{
			"mode":   "webhook",
			"url":    receiver.URL,
			"secret": "whsec_" + strings.Repeat("a", 32),
		},
	})
	resp, err := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`),
		Method: "events/subscribe", Params: subBody,
	})
	require.NoError(t, err)
	require.Nil(t, resp.Error)
	require.Equal(t, 1, q.countForTest("alice", "alert.fired"))

	// Re-subscribe to the same canonical tuple is a refresh, not a
	// new sub — quota count must not double.
	resp, err = srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`2`),
		Method: "events/subscribe", Params: subBody,
	})
	require.NoError(t, err)
	require.Nil(t, resp.Error)
	require.Equal(t, 1, q.countForTest("alice", "alert.fired"),
		"refresh should not increment quota")

	// Unsubscribe → onRemove fires → Release.
	unsubBody, _ := json.Marshal(map[string]any{
		"name":     "alert.fired",
		"delivery": map[string]any{"url": receiver.URL},
	})
	resp, err = srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`3`),
		Method: "events/unsubscribe", Params: unsubBody,
	})
	require.NoError(t, err)
	require.Nil(t, resp.Error)
	require.Equal(t, 0, q.countForTest("alice", "alert.fired"),
		"unsubscribe should release quota slot")
}

func TestQuota_Webhook_TTLPruneReleases(t *testing.T) {
	src, _ := NewYieldingSource[map[string]any](EventDef{
		Name:     "alert.fired",
		Delivery: []string{"webhook"},
	})
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer receiver.Close()

	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	wh := NewWebhookRegistry(
		WithWebhookAllowPrivateNetworks(true),
		WithWebhookTTL(time.Hour),
	)
	q := NewQuota(WithMaxSubscriptionsPerPrincipal("alert.fired", 1))
	Register(Config{
		Sources:                  []EventSource{src},
		Webhooks:                 wh,
		Server:                   srv,
		Quota:                    q,
		UnsafeAnonymousPrincipal: "alice",
	})
	finishInitHandshake(t, srv)

	subscribe := func(variant string) *core.Response {
		body, _ := json.Marshal(map[string]any{
			"name":   "alert.fired",
			"arguments": map[string]any{"variant": variant},
			"delivery": map[string]any{
				"mode":   "webhook",
				"url":    receiver.URL + "/" + variant,
				"secret": "whsec_" + strings.Repeat("a", 32),
			},
		})
		resp, err := srv.Dispatch(context.Background(), &core.Request{
			JSONRPC: "2.0", ID: json.RawMessage(`1`),
			Method: "events/subscribe", Params: body,
		})
		require.NoError(t, err)
		return resp
	}
	require.Nil(t, subscribe("a").Error)
	require.Equal(t, 1, q.countForTest("alice", "alert.fired"))

	// Force expire, then trigger a Register call (with distinct
	// params so Register actually runs through the prune path).
	wh.ExpireAll()
	require.Nil(t, subscribe("b").Error,
		"b should succeed because the TTL prune released a's slot")

	// One was pruned (release), one was added (reserve) → still 1.
	require.Equal(t, 1, q.countForTest("alice", "alert.fired"))
}

func TestQuota_Webhook_OnSubscribeErrorReleases(t *testing.T) {
	src, _ := NewYieldingSource[map[string]any](EventDef{
		Name:     "alert.fired",
		Delivery: []string{"webhook"},
		OnSubscribe: func(HookContext, map[string]any) error {
			return errors.New("upstream busy")
		},
	})
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer receiver.Close()

	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	wh := NewWebhookRegistry(WithWebhookAllowPrivateNetworks(true))
	q := NewQuota(WithMaxSubscriptionsPerPrincipal("alert.fired", 1))
	Register(Config{
		Sources:                  []EventSource{src},
		Webhooks:                 wh,
		Server:                   srv,
		Quota:                    q,
		UnsafeAnonymousPrincipal: "alice",
	})
	finishInitHandshake(t, srv)

	body, _ := json.Marshal(map[string]any{
		"name": "alert.fired",
		"delivery": map[string]any{
			"mode":   "webhook",
			"url":    receiver.URL,
			"secret": "whsec_" + strings.Repeat("a", 32),
		},
	})
	resp, err := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`),
		Method: "events/subscribe", Params: body,
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Error, "on_subscribe error should reject")
	require.Equal(t, 0, q.countForTest("alice", "alert.fired"),
		"failed on_subscribe should release the quota slot via Unregister → onRemove")
}

// --- Push integration ---

func TestQuota_Push_RejectsOverCap(t *testing.T) {
	src, _ := NewYieldingSource[map[string]any](EventDef{
		Name:     "alert.fired",
		Delivery: []string{"push"},
	})
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	q := NewQuota(WithMaxSubscriptionsPerPrincipal("alert.fired", 1))
	Register(Config{
		Sources:                  []EventSource{src},
		Server:                   srv,
		Quota:                    q,
		UnsafeAnonymousPrincipal: "alice",
		StreamHeartbeatInterval:  500 * time.Millisecond,
	})
	finishInitHandshake(t, srv)

	rawReq, _ := json.Marshal(map[string]any{"name": "alert.fired"})
	startStream := func(idx int) (cancel func(), done <-chan struct{}, sawErr <-chan error) {
		ctx, c := context.WithCancel(context.Background())
		d := make(chan struct{})
		errCh := make(chan error, 1)
		go func() {
			defer close(d)
			resp, _ := srv.Dispatch(ctx, &core.Request{
				JSONRPC: "2.0",
				ID:      json.RawMessage([]byte{'"', 's', byte('0' + idx), '"'}),
				Method:  "events/stream",
				Params:  rawReq,
			})
			if resp != nil && resp.Error != nil {
				errCh <- errors.New(resp.Error.Message)
			}
		}()
		return c, d, errCh
	}

	cancelA, doneA, errA := startStream(1)
	defer cancelA()
	// Wait until the first stream has actually Reserved.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) && q.countForTest("alice", "alert.fired") == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	require.Equal(t, 1, q.countForTest("alice", "alert.fired"))

	cancelB, doneB, errB := startStream(2)
	defer cancelB()
	// B should immediately error out without ever holding a slot.
	select {
	case err := <-errB:
		require.Contains(t, err.Error(), "alert.fired")
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("second stream did not return an error within deadline")
	}
	<-doneB

	// First stream still alive; quota count still 1.
	require.Equal(t, 1, q.countForTest("alice", "alert.fired"))
	cancelA()
	<-doneA
	require.Equal(t, 0, q.countForTest("alice", "alert.fired"),
		"defer Release in registerStream should drop the quota count")
	_ = errA
}

// --- Poll integration ---

func TestQuota_Poll_RejectsOverCap(t *testing.T) {
	src, _ := NewYieldingSource[map[string]any](EventDef{
		Name:     "alert.fired",
		Delivery: []string{"poll"},
	})
	leases := NewPollLeaseTable(
		WithPollLeaseTTL(40*time.Millisecond),
		WithPollLeaseSweepInterval(time.Hour), // sweep manually
	)
	defer leases.Close()
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	q := NewQuota(WithMaxSubscriptionsPerPrincipal("alert.fired", 1))
	Register(Config{
		Sources:                  []EventSource{src},
		Server:                   srv,
		PollLeases:               leases,
		Quota:                    q,
		UnsafeAnonymousPrincipal: "alice",
	})
	finishInitHandshake(t, srv)

	poll := func(variant string) *core.Response {
		body, _ := json.Marshal(map[string]any{
			"name":   "alert.fired",
			"arguments": map[string]any{"variant": variant},
		})
		resp, err := srv.Dispatch(context.Background(), &core.Request{
			JSONRPC: "2.0", ID: json.RawMessage(`1`),
			Method: "events/poll", Params: body,
		})
		require.NoError(t, err)
		return resp
	}

	require.Nil(t, poll("a").Error)
	require.Equal(t, 1, q.countForTest("alice", "alert.fired"))

	// Repeat poll for "a" is a renewal — must not increment.
	require.Nil(t, poll("a").Error)
	require.Equal(t, 1, q.countForTest("alice", "alert.fired"),
		"poll renewal should not increment quota")

	// Distinct params → would be a NEW lease → should hit cap.
	resp := poll("b")
	require.NotNil(t, resp.Error)
	require.Equal(t, ErrCodeResourceExhausted, resp.Error.Code)
	// Lease for "b" must NOT have been retained — rollback path.
	require.Equal(t, 1, leases.Len(), "rejected poll should not leave a lease behind")

	// Wait past TTL, sweep → "a" releases → quota slot frees up.
	time.Sleep(60 * time.Millisecond)
	leases.sweepExpiredForTest()
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) && q.countForTest("alice", "alert.fired") != 0 {
		time.Sleep(5 * time.Millisecond)
	}
	require.Equal(t, 0, q.countForTest("alice", "alert.fired"),
		"lease expiry should release quota slot via chained onExpire")

	// Now "b" can succeed.
	require.Nil(t, poll("b").Error)
}

// --- Ordering: Quota fires BEFORE on_subscribe ---

func TestQuota_OnSubscribeNeverFiresWhenAtCap(t *testing.T) {
	var subscribes atomic.Int32
	src, _ := NewYieldingSource[map[string]any](EventDef{
		Name:     "alert.fired",
		Delivery: []string{"webhook"},
		OnSubscribe: func(HookContext, map[string]any) error {
			subscribes.Add(1)
			return nil
		},
	})
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer receiver.Close()

	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	wh := NewWebhookRegistry(WithWebhookAllowPrivateNetworks(true))
	q := NewQuota(WithMaxSubscriptionsPerPrincipal("alert.fired", 1))
	Register(Config{
		Sources:                  []EventSource{src},
		Webhooks:                 wh,
		Server:                   srv,
		Quota:                    q,
		UnsafeAnonymousPrincipal: "alice",
	})
	finishInitHandshake(t, srv)

	subscribe := func(variant string) *core.Response {
		body, _ := json.Marshal(map[string]any{
			"name":   "alert.fired",
			"arguments": map[string]any{"variant": variant},
			"delivery": map[string]any{
				"mode":   "webhook",
				"url":    receiver.URL + "/" + variant,
				"secret": "whsec_" + strings.Repeat("a", 32),
			},
		})
		resp, err := srv.Dispatch(context.Background(), &core.Request{
			JSONRPC: "2.0", ID: json.RawMessage(`1`),
			Method: "events/subscribe", Params: body,
		})
		require.NoError(t, err)
		return resp
	}
	require.Nil(t, subscribe("a").Error)
	require.NotNil(t, subscribe("b").Error)
	if got := subscribes.Load(); got != 1 {
		t.Errorf("on_subscribe fired %d times; want 1 (rejected sub must NOT fire on_subscribe)", got)
	}
}

// countForTest exposes the per-(principal, eventName) count from a
// Quota for assertions. Routes through the QuotaStore's CountQuota
// directly — the seam lift in #626 made counts first-class queryable
// state, so the previous probe-Reserve-Release dance is no longer
// necessary. The wrapper's mu serializes with concurrent
// Reserve/Release in the same test.
func (q *Quota) countForTest(principal, eventName string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	resp, _ := q.store.CountQuota(context.Background(), CountQuotaRequest{
		Principal: principal,
		EventName: eventName,
	})
	return resp.Count
}
