package events

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// streamRoutingStack supports multiple sources behind one server, used to
// exercise per-stream isolation (multiple events/stream calls running
// concurrently against the same session) and gap-recovery wiring.
type streamRoutingStack struct {
	transport *server.InProcessTransport
	notifs    chan capturedNotif
}

func newStreamRoutingStack(t *testing.T, sources []EventSource, heartbeat time.Duration) *streamRoutingStack {
	t.Helper()
	if heartbeat == 0 {
		heartbeat = time.Hour // disable heartbeat unless test asks for it
	}
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	Register(Config{
		Sources:                  sources,
		Webhooks:                 NewWebhookRegistry(),
		Server:                   srv,
		UnsafeAnonymousPrincipal: "test-principal",
		StreamHeartbeatInterval:  heartbeat,
	})

	notifs := make(chan capturedNotif, 256)
	transport := server.NewInProcessTransport(srv, server.WithNotificationHandler(func(method string, params []byte) {
		raw := make([]byte, len(params))
		copy(raw, params)
		select {
		case notifs <- capturedNotif{method: method, params: raw}:
		default:
			t.Logf("notification capture chan full; dropping %s", method)
		}
	}))
	require.NoError(t, transport.Connect(context.Background()))

	initParams := json.RawMessage(`{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`)
	resp, err := transport.Call(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`0`), Method: "initialize", Params: initParams,
	})
	require.NoError(t, err)
	require.Nil(t, resp.Error)
	require.NoError(t, transport.Notify(context.Background(), &core.Request{
		JSONRPC: "2.0", Method: "notifications/initialized",
	}))

	t.Cleanup(func() { _ = transport.Close() })
	return &streamRoutingStack{transport: transport, notifs: notifs}
}

// startStreamID opens an events/stream call with an explicit JSON-RPC id —
// for routing tests we need distinct ids per stream so we can demux
// notifications by requestId.
func (s *streamRoutingStack) startStreamID(t *testing.T, callID json.RawMessage, eventName string) *runningStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	rawParams, _ := json.Marshal(map[string]any{"name": eventName})

	done := make(chan streamResult, 1)
	go func() {
		resp, err := s.transport.Call(ctx, &core.Request{
			JSONRPC: "2.0", ID: callID, Method: "events/stream", Params: rawParams,
		})
		done <- streamResult{resp: resp, err: err}
	}()
	return &runningStream{cancel: cancel, done: done}
}

// requestIDOf decodes the params of a captured notification and returns
// its requestId field as a JSON number string (so tests don't have to
// care about float64 vs int marshalling).
func requestIDOf(t *testing.T, n capturedNotif) string {
	t.Helper()
	var p struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	require.NoError(t, json.Unmarshal(n.params, &p))
	return string(p.RequestID)
}

// TestStream_TwoConcurrentStreamsIsolated verifies the spec's per-stream
// independence guarantee (§"Push-Based Delivery" L271): two events/stream
// calls against DIFFERENT sources must not cross-contaminate. Yields on
// source A must surface only on the stream subscribed to A, never on the
// stream subscribed to B.
//
// Without isolation, the broadcast model from before ε would resurface:
// every open stream would see every event from every source. The
// requestId echo + per-source Subscribe channel are what make this work.
func TestStream_TwoConcurrentStreamsIsolated(t *testing.T) {
	srcA, yieldA := NewYieldingSource[fakePayload](EventDef{Name: "fake.A", Description: "A", Delivery: []string{"push"}})
	srcB, _ := NewYieldingSource[fakePayload](EventDef{Name: "fake.B", Description: "B", Delivery: []string{"push"}})
	st := newStreamRoutingStack(t, []EventSource{srcA, srcB}, 0)

	rsA := st.startStreamID(t, json.RawMessage(`100`), "fake.A")
	defer rsA.endAndAwait(t, time.Second)
	rsB := st.startStreamID(t, json.RawMessage(`200`), "fake.B")
	defer rsB.endAndAwait(t, time.Second)

	// Drain the two initial active notifications.
	expectNotif(t, st.notifs, "notifications/events/active", time.Second)
	expectNotif(t, st.notifs, "notifications/events/active", time.Second)

	// Yield only on source A. Stream A should see one event notif; stream
	// B should see none.
	require.NoError(t, yieldA(fakePayload{Msg: "for-A-only"}))

	// Wait briefly for notification propagation, then drain whatever
	// arrived. We assert: every events/event has requestId=100 (stream A).
	// Heartbeats are disabled, so the only late notifications should be
	// the one event we yielded.
	deadline := time.After(200 * time.Millisecond)
	var aCount, bCount int
	for {
		select {
		case n := <-st.notifs:
			if n.method != "notifications/events/event" {
				continue
			}
			id := requestIDOf(t, n)
			switch id {
			case "100":
				aCount++
			case "200":
				bCount++
			default:
				t.Fatalf("unexpected requestId %q on event notification", id)
			}
		case <-deadline:
			assert.Equal(t, 1, aCount, "stream A should see exactly its yielded event; got %d", aCount)
			assert.Equal(t, 0, bCount, "stream B must not see source-A events; got %d cross-contamination", bCount)
			return
		}
	}
}

// TestStream_TwoConcurrentStreamsSameSourceBothReceive verifies the
// fanout-of-fanout case: two streams against the SAME source both
// receive every yielded event. ε-1's Subscribe slice + the handler
// re-subscribing per call is what makes this work.
//
// Without fanout, only one stream would hear each event (or worse,
// they'd race for the single Subscribe channel).
func TestStream_TwoConcurrentStreamsSameSourceBothReceive(t *testing.T) {
	src, yield := NewYieldingSource[fakePayload](EventDef{Name: "fake.event", Description: "shared", Delivery: []string{"push"}})
	st := newStreamRoutingStack(t, []EventSource{src}, 0)

	rsX := st.startStreamID(t, json.RawMessage(`300`), "fake.event")
	defer rsX.endAndAwait(t, time.Second)
	rsY := st.startStreamID(t, json.RawMessage(`400`), "fake.event")
	defer rsY.endAndAwait(t, time.Second)

	// Drain both initial active notifications.
	expectNotif(t, st.notifs, "notifications/events/active", time.Second)
	expectNotif(t, st.notifs, "notifications/events/active", time.Second)

	require.NoError(t, yield(fakePayload{Msg: "broadcast"}))

	deadline := time.After(time.Second)
	var sawX, sawY bool
	for !sawX || !sawY {
		select {
		case n := <-st.notifs:
			if n.method != "notifications/events/event" {
				continue
			}
			switch requestIDOf(t, n) {
			case "300":
				sawX = true
			case "400":
				sawY = true
			}
		case <-deadline:
			t.Fatalf("not all streams received the event: sawX=%v sawY=%v", sawX, sawY)
		}
	}
}

// fakeSubscribableSource is a hand-built EventSource that lets tests
// inject SubscriberEvents directly — including Truncated=true markers
// that are otherwise hard to force through the normal yield path
// without racy slow-consumer setups. Used only by gap-recovery tests.
type fakeSubscribableSource struct {
	def    EventDef
	ch     chan SubscriberEvent
	latest string
}

func (f *fakeSubscribableSource) Def() EventDef                  { return f.def }
func (f *fakeSubscribableSource) Poll(string, int) PollResult    { return PollResult{} }
func (f *fakeSubscribableSource) Latest() string                 { return f.latest }
func (f *fakeSubscribableSource) Subscribe(context.Context) <-chan SubscriberEvent {
	return f.ch
}

// TestStream_GapRecoveryEmitsFreshActive verifies the spec L285 contract:
// when a SubscriberEvent arrives with Truncated=true (the source dropped
// events because the consumer fell behind), the stream handler emits a
// fresh notifications/events/active{truncated:true, cursor:source.Latest()}
// BEFORE the recovery event itself. This is what tells the client "you
// missed events; reset your cursor to this fresh value."
//
// Without the fresh active, a slow client would receive the recovery event
// silently and not know to re-fetch state.
func TestStream_GapRecoveryEmitsFreshActive(t *testing.T) {
	cursor := "42"
	fake := &fakeSubscribableSource{
		def:    EventDef{Name: "fake.event", Description: "test", Delivery: []string{"push"}},
		ch:     make(chan SubscriberEvent, 4),
		latest: "fresh-cursor-after-gap",
	}
	st := newStreamRoutingStack(t, []EventSource{fake}, 0)

	rs := st.startStreamID(t, json.RawMessage(`500`), "fake.event")
	defer rs.endAndAwait(t, time.Second)

	// Drain the initial active.
	first := expectNotif(t, st.notifs, "notifications/events/active", time.Second)
	var firstP map[string]any
	require.NoError(t, json.Unmarshal(first.params, &firstP))
	_, hasTrunc := firstP["truncated"]
	assert.False(t, hasTrunc, "initial active must not carry truncated:true")

	// Inject a recovery event marked Truncated=true. The handler must
	// emit a fresh active{truncated:true} BEFORE the event notification.
	fake.ch <- SubscriberEvent{
		Truncated: true,
		Event: Event{
			EventID: "evt_recovery", Name: "fake.event",
			Timestamp: "t", Data: json.RawMessage(`{}`),
			Cursor: &cursor,
		},
	}

	// Next active must be the fresh one with truncated:true and the
	// source's current latest cursor.
	freshActive := expectNotif(t, st.notifs, "notifications/events/active", time.Second)
	var p map[string]any
	require.NoError(t, json.Unmarshal(freshActive.params, &p))
	assert.Equal(t, true, p["truncated"], "fresh active must carry truncated:true")
	assert.Equal(t, "fresh-cursor-after-gap", p["cursor"],
		"fresh active cursor MUST be source.Latest() per spec L285, not the (stale) original")

	// And the event notification follows.
	ev := expectNotif(t, st.notifs, "notifications/events/event", time.Second)
	var evP map[string]any
	require.NoError(t, json.Unmarshal(ev.params, &evP))
	assert.Equal(t, "evt_recovery", evP["eventId"])
}
