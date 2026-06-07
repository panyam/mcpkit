package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestStack(t *testing.T) (*httptest.Server, *wiredServer) {
	t.Helper()
	w := buildServer(":0")
	handler := w.srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts, w
}

func newTestClient(t *testing.T, ts *httptest.Server) *client.Client {
	t.Helper()
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"})
	require.NoError(t, c.Connect())
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// drainStream collects events until either n events have been received or the
// context fires. Safe for concurrent receivers — each test gets its own ch.
func drainStream(ctx context.Context, ch <-chan events.Event, n int) []events.Event {
	var out []events.Event
	for len(out) < n {
		select {
		case <-ctx.Done():
			return out
		case ev := <-ch:
			out = append(out, ev)
		}
	}
	return out
}

func TestMatch_ChannelFiltering_RoutesOnlyMatchingEventsToEachSub(t *testing.T) {
	ts, w := newTestStack(t)
	clientA := newTestClient(t, ts)
	clientB := newTestClient(t, ts)

	chA := make(chan events.Event, 8)
	chB := make(chan events.Event, 8)

	streamA, err := eventsclient.Stream(context.Background(), clientA, eventsclient.StreamOptions{
		EventName: "chat.message",
		Params:    map[string]any{"channel": "general"},
		OnEvent:   func(ev events.Event) { chA <- ev },
	})
	require.NoError(t, err)
	defer streamA.Stop()

	streamB, err := eventsclient.Stream(context.Background(), clientB, eventsclient.StreamOptions{
		EventName: "chat.message",
		Params:    map[string]any{"channel": "dev"},
		OnEvent:   func(ev events.Event) { chB <- ev },
	})
	require.NoError(t, err)
	defer streamB.Stop()

	require.NoError(t, injectChat(w.chatYield, "general", "alice", "hi general"))
	require.NoError(t, injectChat(w.chatYield, "dev", "bob", "hi dev"))
	require.NoError(t, injectChat(w.chatYield, "alerts", "carol", "irrelevant"))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	gotA := drainStream(ctx, chA, 1)
	gotB := drainStream(ctx, chB, 1)
	require.Len(t, gotA, 1, "sub A should receive exactly its channel match")
	require.Len(t, gotB, 1, "sub B should receive exactly its channel match")

	var a, b ChatMessageData
	require.NoError(t, json.Unmarshal(gotA[0].Data, &a))
	require.NoError(t, json.Unmarshal(gotB[0].Data, &b))
	assert.Equal(t, "general", a.Channel)
	assert.Equal(t, "dev", b.Channel)

	// And no cross-talk after a brief wait: the 'alerts' event must not
	// have hit either subscriber's stream.
	select {
	case ev := <-chA:
		t.Fatalf("sub A received an unexpected event: %s", string(ev.Data))
	case ev := <-chB:
		t.Fatalf("sub B received an unexpected event: %s", string(ev.Data))
	case <-time.After(250 * time.Millisecond):
	}
}

func TestTransform_RedactsPII_OnlyForOptedInSubscriber(t *testing.T) {
	ts, w := newTestStack(t)
	clientPlain := newTestClient(t, ts)
	clientRedact := newTestClient(t, ts)

	chPlain := make(chan events.Event, 4)
	chRedact := make(chan events.Event, 4)

	streamPlain, err := eventsclient.Stream(context.Background(), clientPlain, eventsclient.StreamOptions{
		EventName: "alert.fired",
		Params:    map[string]any{"severity": "P1", "redact_pii": false},
		OnEvent:   func(ev events.Event) { chPlain <- ev },
	})
	require.NoError(t, err)
	defer streamPlain.Stop()

	streamRedact, err := eventsclient.Stream(context.Background(), clientRedact, eventsclient.StreamOptions{
		EventName: "alert.fired",
		Params:    map[string]any{"severity": "P1", "redact_pii": true},
		OnEvent:   func(ev events.Event) { chRedact <- ev },
	})
	require.NoError(t, err)
	defer streamRedact.Stop()

	require.NoError(t, injectAlert(w.alertYield, "P1", "api-gateway", "alice",
		"latency spike — page alice@example.com"))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	plainEvents := drainStream(ctx, chPlain, 1)
	redactEvents := drainStream(ctx, chRedact, 1)
	require.Len(t, plainEvents, 1)
	require.Len(t, redactEvents, 1)

	var plain, redact AlertData
	require.NoError(t, json.Unmarshal(plainEvents[0].Data, &plain))
	require.NoError(t, json.Unmarshal(redactEvents[0].Data, &redact))

	assert.Equal(t, "alice", plain.Reporter, "non-redacted sub must see the original reporter")
	assert.Contains(t, plain.Message, "alice@example.com", "non-redacted sub must see the original email")

	assert.Empty(t, redact.Reporter, "redacted sub must see reporter cleared")
	assert.NotContains(t, redact.Message, "alice@example.com", "redacted sub must have the email stripped")
	assert.Contains(t, redact.Message, "<redacted-email>", "redaction should leave a placeholder marker")
}

func TestMatch_FiltersBeforeTransform(t *testing.T) {
	// A subscriber with severity:"P1" should never see a P2 event,
	// regardless of redact_pii. Pins ordering: Match gates first.
	ts, w := newTestStack(t)
	c := newTestClient(t, ts)

	ch := make(chan events.Event, 4)
	stream, err := eventsclient.Stream(context.Background(), c, eventsclient.StreamOptions{
		EventName: "alert.fired",
		Params:    map[string]any{"severity": "P1", "redact_pii": true},
		OnEvent:   func(ev events.Event) { ch <- ev },
	})
	require.NoError(t, err)
	defer stream.Stop()

	require.NoError(t, injectAlert(w.alertYield, "P2", "api-gateway", "alice", "p2 noise"))
	require.NoError(t, injectAlert(w.alertYield, "P1", "api-gateway", "alice", "p1 match"))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got := drainStream(ctx, ch, 1)
	require.Len(t, got, 1, "only one event (the P1) should reach the sub")
	var alert AlertData
	require.NoError(t, json.Unmarshal(got[0].Data, &alert))
	assert.Equal(t, "P1", alert.Severity)
}

func TestOnSubscribe_RegistersWatchList(t *testing.T) {
	_, w := newTestStack(t)
	c := newTestClient(t, newTestServerWithStack(t, w))

	stream, err := eventsclient.Stream(context.Background(), c, eventsclient.StreamOptions{
		EventName: "presence.changed",
		Params:    map[string]any{"watch_users": []any{"alice", "bob"}},
		OnEvent:   func(_ events.Event) {},
	})
	require.NoError(t, err)
	defer stream.Stop()

	// Wait briefly for the subscribe round-trip + OnSubscribe to land.
	require.Eventually(t, func() bool {
		w.registry.mu.Lock()
		defer w.registry.mu.Unlock()
		return len(w.registry.byEntry) == 1
	}, time.Second, 20*time.Millisecond, "OnSubscribe must have populated the watch-list registry")
}

// newTestServerWithStack builds a fresh httptest server bound to the
// stack `w`. Used by tests that need to register more clients against
// an existing wiredServer instance.
func newTestServerWithStack(t *testing.T, w *wiredServer) *httptest.Server {
	t.Helper()
	handler := w.srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts
}

func TestEmitToSubscription_RoutesOnlyToWatchingSubscriptions(t *testing.T) {
	ts, w := newTestStack(t)
	clientE := newTestClient(t, ts)
	clientF := newTestClient(t, ts)

	chE := make(chan events.Event, 8)
	chF := make(chan events.Event, 8)

	streamE, err := eventsclient.Stream(context.Background(), clientE, eventsclient.StreamOptions{
		EventName: "presence.changed",
		Params:    map[string]any{"watch_users": []any{"alice"}},
		OnEvent:   func(ev events.Event) { chE <- ev },
	})
	require.NoError(t, err)
	defer streamE.Stop()

	streamF, err := eventsclient.Stream(context.Background(), clientF, eventsclient.StreamOptions{
		EventName: "presence.changed",
		Params:    map[string]any{"watch_users": []any{"bob"}},
		OnEvent:   func(ev events.Event) { chF <- ev },
	})
	require.NoError(t, err)
	defer streamF.Stop()

	// Wait for both OnSubscribe to land.
	require.Eventually(t, func() bool {
		w.registry.mu.Lock()
		defer w.registry.mu.Unlock()
		return len(w.registry.byEntry) == 2
	}, time.Second, 20*time.Millisecond)

	// Emit one presence transition for alice; only sub E should receive.
	emitPresence(w.idx, w.registry, PresenceChangedData{
		User: "alice", State: "online", Timestamp: time.Now().UTC().Format(time.RFC3339),
	})
	// And one for bob; only sub F should receive.
	emitPresence(w.idx, w.registry, PresenceChangedData{
		User: "bob", State: "away", Timestamp: time.Now().UTC().Format(time.RFC3339),
	})
	// And one for carol; NEITHER should receive (no watcher).
	emitPresence(w.idx, w.registry, PresenceChangedData{
		User: "carol", State: "offline", Timestamp: time.Now().UTC().Format(time.RFC3339),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	gotE := drainStream(ctx, chE, 1)
	gotF := drainStream(ctx, chF, 1)
	require.Len(t, gotE, 1)
	require.Len(t, gotF, 1)

	var e, f PresenceChangedData
	require.NoError(t, json.Unmarshal(gotE[0].Data, &e))
	require.NoError(t, json.Unmarshal(gotF[0].Data, &f))
	assert.Equal(t, "alice", e.User)
	assert.Equal(t, "bob", f.User)

	// Carol's transition reached nobody.
	select {
	case ev := <-chE:
		t.Fatalf("sub E received unexpected event: %s", string(ev.Data))
	case ev := <-chF:
		t.Fatalf("sub F received unexpected event: %s", string(ev.Data))
	case <-time.After(250 * time.Millisecond):
	}
}

func TestOnUnsubscribe_ClearsWatchList(t *testing.T) {
	ts, w := newTestStack(t)
	c := newTestClient(t, ts)

	stream, err := eventsclient.Stream(context.Background(), c, eventsclient.StreamOptions{
		EventName: "presence.changed",
		Params:    map[string]any{"watch_users": []any{"alice"}},
		OnEvent:   func(_ events.Event) {},
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		w.registry.mu.Lock()
		defer w.registry.mu.Unlock()
		return len(w.registry.byEntry) == 1
	}, time.Second, 20*time.Millisecond)

	stream.Stop()

	require.Eventually(t, func() bool {
		w.registry.mu.Lock()
		defer w.registry.mu.Unlock()
		return len(w.registry.byEntry) == 0
	}, time.Second, 20*time.Millisecond, "OnUnsubscribe must clear the registry entry")
}

func TestQuota_RejectsBeyondCapWithStructuredError(t *testing.T) {
	ts, _ := newTestStack(t)

	// Three webhook subs (no Stream cleanup races): the cap on
	// chat.message is quotaCap = 2; the third subscribe must fail.
	type subResult struct {
		err error
		sub *eventsclient.Subscription
	}
	results := make([]subResult, 0, 3)
	var mu sync.Mutex

	wg := sync.WaitGroup{}
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := newTestClient(t, ts)
			recv := httptest.NewServer(eventsclient.NewReceiver[ChatMessageData]("whsec_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))
			t.Cleanup(recv.Close)
			sub, err := eventsclient.Subscribe(context.Background(), c, eventsclient.SubscribeOptions{
				EventName:   "chat.message",
				CallbackURL: recv.URL,
			})
			mu.Lock()
			results = append(results, subResult{err: err, sub: sub})
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	for _, r := range results {
		if r.sub != nil {
			defer r.sub.Stop()
		}
	}

	var failures int
	var rpc *client.RPCError
	for _, r := range results {
		if r.err == nil {
			continue
		}
		failures++
		if rpc == nil {
			rpc = unwrapRPC(r.err)
		}
	}
	require.GreaterOrEqual(t, failures, 1, "at least one subscribe beyond cap=%d must fail", quotaCap)
	require.NotNil(t, rpc, "expected at least one *client.RPCError among failures, got: %+v", results)
	assert.Equal(t, -32013, rpc.Code, "expected -32013 TooManySubscriptions per spec")
	// Wire shape: library quota error data carries {limit: "subscriptions", max: <N>}.
	// Pinning this here means a downstream consumer (407 stage-4 walkthrough,
	// future #635 consistency follow-up) can compare against a known shape.
	dataMap, ok := rpc.Data.(map[string]any)
	require.True(t, ok, "rpc.Data should be a map, got %T", rpc.Data)
	assert.Equal(t, "subscriptions", dataMap["limit"], "spec quota error data should include limit")
	assert.EqualValues(t, quotaCap, dataMap["max"], "spec quota error data should include max=cap configured")
}

func unwrapRPC(err error) *client.RPCError {
	for err != nil {
		if rpc, ok := err.(*client.RPCError); ok {
			return rpc
		}
		un, ok := err.(interface{ Unwrap() error })
		if !ok {
			return nil
		}
		err = un.Unwrap()
	}
	return nil
}
