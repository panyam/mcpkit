package events

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for spec commit 28ec35e9 (2026-09-04), §"Dynamic Event Types" and
// §"Event Type Removal and Breaking Changes":
//
//   - notifications/events/list_changed on AddSource / RemoveSource
//   - live subscriptions terminated when their event type goes away,
//     carrying -32011 NotFound with data.kind "event"
//
// Before this, RemoveSource deleted the map entry and told nobody. A
// webhook subscriber kept a subscription the server would never deliver
// on again, and a push subscriber's stream closed with no reason given.

// captureBroadcast records every Broadcast the server makes so a test can
// assert on notifications without standing up a transport.
type captureBroadcast struct {
	mu      sync.Mutex
	methods []string
}

func (c *captureBroadcast) Publish(ctx context.Context, method string, params any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.methods = append(c.methods, method)
}

func (c *captureBroadcast) count(method string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, m := range c.methods {
		if m == method {
			n++
		}
	}
	return n
}

type removableSource struct{ name string }

func (r removableSource) Def() EventDef {
	return EventDef{Name: r.name, Delivery: []string{"poll", "push", "webhook"}}
}
func (removableSource) Poll(cursor string, limit int) PollResult { return PollResult{} }
func (removableSource) Latest() string                           { return "" }

func buildRemovalStack(t *testing.T) (*Registry, *WebhookRegistry, *captureBroadcast) {
	t.Helper()
	cap := &captureBroadcast{}
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"},
		server.WithNotificationRelay(cap))
	webhooks := NewWebhookRegistry(WithWebhookAllowPrivateNetworks(true), WithUnsafeWebhookAllowPlaintextCallbacks())
	reg := Register(Config{
		Sources:                  []EventSource{},
		Webhooks:                 webhooks,
		Server:                   srv,
		UnsafeAnonymousPrincipal: "test-principal",
	})
	return reg, webhooks, cap
}

// TestAddSource_BroadcastsListChanged covers §"Dynamic Event Types": a
// client whose registry is stale has no way to learn a new event type
// exists without this ping.
func TestAddSource_BroadcastsListChanged(t *testing.T) {
	reg, _, cap := buildRemovalStack(t)
	require.NoError(t, reg.AddSource(removableSource{name: "new.event"}))
	assert.Equal(t, 1, cap.count("notifications/events/list_changed"),
		"AddSource must announce the catalog change (spec §Dynamic Event Types)")
}

// TestRemoveSource_BroadcastsListChanged is the other half. Removal changes
// the advertised set just as much as addition does.
func TestRemoveSource_BroadcastsListChanged(t *testing.T) {
	reg, _, cap := buildRemovalStack(t)
	require.NoError(t, reg.AddSource(removableSource{name: "doomed.event"}))
	require.NoError(t, reg.RemoveSource("doomed.event"))
	assert.Equal(t, 2, cap.count("notifications/events/list_changed"),
		"both AddSource and RemoveSource must announce; got %v", cap.methods)
}

// TestFailedAddSource_DoesNotBroadcast guards against announcing a change
// that did not happen. A duplicate registration is rejected, so clients
// must not be told to re-read a catalog that is identical.
func TestFailedAddSource_DoesNotBroadcast(t *testing.T) {
	reg, _, cap := buildRemovalStack(t)
	require.NoError(t, reg.AddSource(removableSource{name: "dup.event"}))
	require.Error(t, reg.AddSource(removableSource{name: "dup.event"}))
	assert.Equal(t, 1, cap.count("notifications/events/list_changed"),
		"a rejected AddSource must not announce a catalog change")
}

// TestFailedRemoveSource_DoesNotBroadcast is the same guard on the other side.
func TestFailedRemoveSource_DoesNotBroadcast(t *testing.T) {
	reg, _, cap := buildRemovalStack(t)
	require.Error(t, reg.RemoveSource("never.registered"))
	assert.Zero(t, cap.count("notifications/events/list_changed"),
		"a rejected RemoveSource must not announce a catalog change")
}

// TestRemoveSource_TerminatesWebhookSubscriptions is the core of
// §"Event Type Removal and Breaking Changes". The receiver must get a
// terminated envelope, and the registry must drop the subscription, or the
// client keeps refreshing a subscription that can never fire again.
func TestRemoveSource_TerminatesWebhookSubscriptions(t *testing.T) {
	reg, webhooks, _ := buildRemovalStack(t)
	require.NoError(t, reg.AddSource(removableSource{name: "doomed.event"}))

	got := make(chan []byte, 4)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		got <- buf
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	webhooks.Register(RegisterParams{
		CanonicalKey: []byte("doomed-key"),
		DerivedID:    "sub_doomed",
		URL:          receiver.URL,
		Secret:       generateSecret(),
		EventName:    "doomed.event",
		Principal:    "test-principal",
	})
	require.Len(t, webhooks.Targets(), 1)

	require.NoError(t, reg.RemoveSource("doomed.event"))

	select {
	case body := <-got:
		var env struct {
			Type  string `json:"type"`
			Error struct {
				Code int            `json:"code"`
				Data map[string]any `json:"data"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal(body, &env))
		assert.Equal(t, "terminated", env.Type)
		assert.Equal(t, ErrCodeNotFound, env.Error.Code,
			"removal terminates with -32011 NotFound, not -32012 Forbidden: the principal's access did not change")
		assert.Equal(t, "event", env.Error.Data["kind"],
			"data.kind must be \"event\" so the client can distinguish a removed type from a missing subscription")
	case <-time.After(3 * time.Second):
		t.Fatal("no terminated envelope delivered after RemoveSource")
	}

	assert.Empty(t, webhooks.Targets(),
		"terminated subscriptions must also be dropped from the registry")
}

// TestRemoveSource_LeavesOtherEventsSubscriptionsAlone is the blast-radius
// test. Terminating by event name must not catch subscriptions to names
// that still exist.
func TestRemoveSource_LeavesOtherEventsSubscriptionsAlone(t *testing.T) {
	reg, webhooks, _ := buildRemovalStack(t)
	require.NoError(t, reg.AddSource(removableSource{name: "doomed.event"}))
	require.NoError(t, reg.AddSource(removableSource{name: "survivor.event"}))

	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	for _, name := range []string{"doomed.event", "survivor.event"} {
		webhooks.Register(RegisterParams{
			CanonicalKey: []byte(name + "-key"),
			DerivedID:    "sub_" + name,
			URL:          receiver.URL,
			Secret:       generateSecret(),
			EventName:    name,
			Principal:    "test-principal",
		})
	}
	require.Len(t, webhooks.Targets(), 2)

	require.NoError(t, reg.RemoveSource("doomed.event"))

	remaining := webhooks.Targets()
	require.Len(t, remaining, 1, "only the removed type's subscriptions may be terminated")
	assert.Equal(t, "survivor.event", remaining[0].EventName)
}

// TestRemoveSource_TerminatesPushSubscribers covers the push half. A
// YieldingSource can signal its live stream subscribers; the terminal
// SubscriberEvent is what the stream handler turns into a
// notifications/events/terminated frame.
func TestRemoveSource_TerminatesPushSubscribers(t *testing.T) {
	reg, _, _ := buildRemovalStack(t)
	src, _ := NewYieldingSource[map[string]any](EventDef{
		Name:     "push.event",
		Delivery: []string{"push"},
	})
	require.NoError(t, reg.AddSource(src))

	ch, _ := src.Subscribe(context.Background(), SubscribeOpts{})
	require.NoError(t, reg.RemoveSource("push.event"))

	select {
	case se, ok := <-ch:
		require.True(t, ok, "subscriber channel closed without a terminal signal")
		require.NotNil(t, se.Terminated, "push subscribers must receive a terminal signal on removal")
		assert.Equal(t, ErrCodeNotFound, se.Terminated.Code)
		nf, ok := se.Terminated.Data.(NotFoundData)
		require.True(t, ok, "terminal signal must carry NotFoundData; got %#v", se.Terminated.Data)
		assert.Equal(t, "event", nf.Kind)
	case <-time.After(3 * time.Second):
		t.Fatal("no terminal signal delivered to push subscriber after RemoveSource")
	}
}

// TestRemoveSource_NonTerminatableSourceStillSucceeds keeps removal working
// for sources that cannot signal their subscribers. The alternative, failing
// the removal, would make a plain EventSource impossible to unregister.
func TestRemoveSource_NonTerminatableSourceStillSucceeds(t *testing.T) {
	reg, _, cap := buildRemovalStack(t)
	require.NoError(t, reg.AddSource(removableSource{name: "plain.event"}))
	assert.NoError(t, reg.RemoveSource("plain.event"),
		"a source that cannot terminate its subscribers must still be removable")
	assert.Equal(t, 2, cap.count("notifications/events/list_changed"))
}

// TestUnsupportedData_CarriesSchemaChangedReason pins the -32014 payload
// shape from §"Event Type Removal and Breaking Changes". mcpkit has no
// in-place descriptor mutation API yet, so nothing in the library emits
// this today; the shape is here so an author terminating a subscription by
// hand can express it, and so the conformance suite has something to check.
func TestUnsupportedData_CarriesSchemaChangedReason(t *testing.T) {
	raw, err := json.Marshal(UnsupportedData{
		Feature: "payloadSchema",
		Reason:  ReasonSchemaChanged,
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"feature":"payloadSchema","reason":"schema_changed"}`, string(raw))
}

// TestUnsupportedData_OmitsReasonWhenUnset keeps the existing -32014 wire
// unchanged for the delivery-mode rejections that already use it.
func TestUnsupportedData_OmitsReasonWhenUnset(t *testing.T) {
	raw, err := json.Marshal(UnsupportedData{Feature: "deliveryMode", Value: "push"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"feature":"deliveryMode","value":"push"}`, string(raw))
}
