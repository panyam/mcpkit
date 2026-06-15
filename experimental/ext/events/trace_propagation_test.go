package events

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	demoTraceparent  = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	demoTraceparent2 = "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-1111111111111111-01"
	demoTracestate   = "vendor=val,other=x"
)

func ctxWithTraceparent(tp string) context.Context {
	return core.WithTraceContext(context.Background(), core.TraceContext{Traceparent: tp})
}

// Gate 1+2: yield(ctx, data) stamps event.Meta.traceparent and the
// emit hook receives ctx with the same trace context attached.
func TestYieldCtx_StampsMetaAndFlowsToHook(t *testing.T) {
	src, yield := NewYieldingSource[fakePayload](EventDef{Name: "fake"})

	var (
		hookEvent Event
		hookCtx   context.Context
		done      = make(chan struct{})
	)
	src.SetEmitHook(func(ctx context.Context, e Event) {
		hookCtx = ctx
		hookEvent = e
		close(done)
	})

	require.NoError(t, yield(ctxWithTraceparent(demoTraceparent), fakePayload{Msg: "hi"}))
	<-done

	require.NotNil(t, hookEvent.Meta, "yield with ctx carrying traceparent must populate Event.Meta")
	assert.Equal(t, demoTraceparent, hookEvent.Meta[core.MetaKeyTraceparent],
		"event.Meta must carry the traceparent (persistent carrier for cross-process consumers)")
	assert.Equal(t, demoTraceparent, core.TraceContextFromContext(hookCtx).Traceparent,
		"emit hook must receive ctx with the same trace context (in-process carrier)")
}

func TestYieldCtx_NoTraceContext_NoStamp(t *testing.T) {
	src, yield := NewYieldingSource[fakePayload](EventDef{Name: "fake"})
	var got Event
	done := make(chan struct{})
	src.SetEmitHook(func(_ context.Context, e Event) {
		got = e
		close(done)
	})

	require.NoError(t, yield(context.Background(), fakePayload{Msg: "hi"}))
	<-done

	if got.Meta != nil {
		_, has := got.Meta[core.MetaKeyTraceparent]
		assert.False(t, has, "background ctx must not stamp a traceparent into Meta")
	}
}

// metaFunc setting traceparent wins over the yield-time auto-stamp —
// matches the caller-preserves semantic used by core.InjectTraceContextIntoParams
// and the Apps Bridge TS-side relay (PR 702).
func TestYieldCtx_MetaFuncSetTraceparent_NotClobbered(t *testing.T) {
	src, yield := NewYieldingSource[fakePayload](EventDef{Name: "fake"})
	src.SetMetaFunc(func(_ context.Context, _ fakePayload) map[string]any {
		return map[string]any{core.MetaKeyTraceparent: demoTraceparent2}
	})
	var got Event
	done := make(chan struct{})
	src.SetEmitHook(func(_ context.Context, e Event) {
		got = e
		close(done)
	})

	require.NoError(t, yield(ctxWithTraceparent(demoTraceparent), fakePayload{Msg: "hi"}))
	<-done

	assert.Equal(t, demoTraceparent2, got.Meta[core.MetaKeyTraceparent],
		"metaFunc-set traceparent must be preserved; the yield-time auto-stamp is a fallback only")
}

func TestYieldCtx_TracestateAlongsideTraceparent(t *testing.T) {
	src, yield := NewYieldingSource[fakePayload](EventDef{Name: "fake"})
	var got Event
	done := make(chan struct{})
	src.SetEmitHook(func(_ context.Context, e Event) {
		got = e
		close(done)
	})

	ctx := core.WithTraceContext(context.Background(), core.TraceContext{
		Traceparent: demoTraceparent,
		Tracestate:  demoTracestate,
	})
	require.NoError(t, yield(ctx, fakePayload{Msg: "hi"}))
	<-done

	assert.Equal(t, demoTraceparent, got.Meta[core.MetaKeyTraceparent])
	assert.Equal(t, demoTracestate, got.Meta[core.MetaKeyTracestate])
}

// Gate 3: WebhookRegistry.Deliver injects the W3C traceparent HTTP
// header on every outbound POST when ctx (or event.Meta as a
// fallback) carries one.
func TestWebhookDeliver_InjectsTraceparentHeader(t *testing.T) {
	var (
		mu              sync.Mutex
		seenTraceparent string
		seenTracestate  string
	)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenTraceparent = r.Header.Get("traceparent")
		seenTracestate = r.Header.Get("tracestate")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	wh := newWebhookRegistryWithTarget(t, receiver.URL)

	ctx := core.WithTraceContext(context.Background(), core.TraceContext{
		Traceparent: demoTraceparent,
		Tracestate:  demoTracestate,
	})
	event := MakeEvent("fake.event", "evt_relay_1", "1", time.Now(), map[string]string{"k": "v"})
	wh.Deliver(ctx, event)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return seenTraceparent != ""
	}, 3*time.Second, 20*time.Millisecond, "receiver should see the traceparent header within retry window")

	assert.Equal(t, demoTraceparent, seenTraceparent)
	assert.Equal(t, demoTracestate, seenTracestate)
}

func TestWebhookDeliver_NoTraceContext_NoHeader(t *testing.T) {
	var (
		mu          sync.Mutex
		seenHeaders http.Header
		hits        int
	)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenHeaders = r.Header.Clone()
		hits++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	wh := newWebhookRegistryWithTarget(t, receiver.URL)
	event := MakeEvent("fake.event", "evt_no_trace", "1", time.Now(), map[string]string{"k": "v"})
	wh.Deliver(context.Background(), event)

	require.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return hits >= 1 },
		2*time.Second, 20*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.Empty(t, seenHeaders.Get("traceparent"),
		"no traceparent in ctx or event.Meta must mean no traceparent header on the wire")
}

// event.Meta carries the traceparent on the fallback path — used by
// callers that haven't been threaded with ctx but pre-stamped Meta
// themselves (e.g., persisted-and-replayed events).
func TestWebhookDeliver_FallsBackToEventMeta(t *testing.T) {
	var (
		mu              sync.Mutex
		seenTraceparent string
	)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenTraceparent = r.Header.Get("traceparent")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	wh := newWebhookRegistryWithTarget(t, receiver.URL)
	event := MakeEvent("fake.event", "evt_meta_only", "1", time.Now(), map[string]string{"k": "v"})
	event.Meta = map[string]any{core.MetaKeyTraceparent: demoTraceparent}
	// Context has NO trace context — exercises the event.Meta fallback path.
	wh.Deliver(context.Background(), event)

	require.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return seenTraceparent != "" },
		3*time.Second, 20*time.Millisecond)

	assert.Equal(t, demoTraceparent, seenTraceparent,
		"event.Meta.traceparent must be picked up when ctx has none (replayed-event path)")
}

// Gate 4: HTTPSource.serveInject extracts the W3C traceparent HTTP
// header from inbound POSTs and threads it through into yield so
// Event.Meta carries the trace ID. Closes the round-trip with Gate 3.
func TestHTTPSourceInject_ExtractsTraceparentFromHTTPHeader(t *testing.T) {
	src := NewHTTPSource[fakePayload](EventDef{Name: "chat"}, HTTPSourceConfig{})

	var (
		got  Event
		done = make(chan struct{})
	)
	src.SetEmitHook(func(_ context.Context, e Event) {
		got = e
		close(done)
	})

	ts := httptest.NewServer(src.Handler())
	defer ts.Close()

	body, _ := json.Marshal(fakePayload{Msg: "hi from peer A"})
	req, err := http.NewRequest("POST", ts.URL, strings.NewReader(string(body)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("traceparent", demoTraceparent)
	req.Header.Set("tracestate", demoTracestate)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	_ = resp.Body.Close()

	<-done
	require.NotNil(t, got.Meta, "yield-from-HTTP must populate Event.Meta with the inbound traceparent")
	assert.Equal(t, demoTraceparent, got.Meta[core.MetaKeyTraceparent],
		"inbound traceparent header → event.Meta.traceparent — closes the cross-process round-trip")
	assert.Equal(t, demoTracestate, got.Meta[core.MetaKeyTracestate])
}

func TestHTTPSourceInject_NoTraceparent_NoStamp(t *testing.T) {
	src := NewHTTPSource[fakePayload](EventDef{Name: "chat"}, HTTPSourceConfig{})
	var got Event
	done := make(chan struct{})
	src.SetEmitHook(func(_ context.Context, e Event) {
		got = e
		close(done)
	})

	ts := httptest.NewServer(src.Handler())
	defer ts.Close()

	body, _ := json.Marshal(fakePayload{Msg: "no trace"})
	resp, err := http.Post(ts.URL, "application/json", strings.NewReader(string(body)))
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	_ = resp.Body.Close()

	<-done
	if got.Meta != nil {
		_, has := got.Meta[core.MetaKeyTraceparent]
		assert.False(t, has, "no inbound traceparent header → no traceparent on Event.Meta")
	}
}

// newWebhookRegistryWithTarget spins up a minimal WebhookRegistry +
// registers one target pointing at receiverURL. Shared helper for the
// trace-relay tests (each tests the same outbound delivery shape).
func newWebhookRegistryWithTarget(t *testing.T, receiverURL string) *WebhookRegistry {
	t.Helper()
	wh := NewWebhookRegistry(WithWebhookAllowPrivateNetworks(true))
	target := WebhookTarget{
		EventName: "fake.event",
		URL:       receiverURL,
		Secret:    generateSecret(),
		ExpiresAt: ptrTime(time.Now().Add(time.Hour)),
		Status:    DeliveryStatus{Active: true},
	}
	target.CanonicalKey = canonicalKey("test-principal", receiverURL, "fake.event", nil)
	target.ID = deriveSubscriptionID(target.CanonicalKey)
	_, err := wh.store.SaveWebhook(context.Background(), SaveWebhookRequest{Target: target})
	require.NoError(t, err)
	return wh
}
