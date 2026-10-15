package events

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// listenStack serves one events source on the 2026-07-28 wire and returns the
// registry plus a stateless client, so a test can open subscriptions/listen and
// change the catalog underneath it.
func listenStack(t *testing.T) (*Registry, *client.Client) {
	t.Helper()
	src, _ := NewYieldingSource[fakePayload](EventDef{Name: "fake.event", Description: "test", Delivery: []string{"poll"}})
	srv := server.NewServer(core.ServerInfo{Name: "listen-events", Version: "0.1.0"})
	reg := Register(Config{
		Sources:                  []EventSource{src},
		Webhooks:                 NewWebhookRegistry(),
		Server:                   srv,
		UnsafeAnonymousPrincipal: "test-anon",
	})
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "t", Version: "1"}, client.WithClientMode(client.ClientModeStateless))
	require.NoError(t, c.Connect(t.Context()))
	t.Cleanup(func() { c.Close() })
	return reg, c
}

func addFakeSource(t *testing.T, reg *Registry, name string) {
	t.Helper()
	src, _ := NewYieldingSource[fakePayload](EventDef{Name: name, Description: "added at runtime", Delivery: []string{"poll"}})
	require.NoError(t, reg.AddSource(src))
}

// SEP-3415: list_changed "is delivered only on a subscriptions/listen stream,
// and only when the client opted in", and the server "echoes eventsListChanged
// in notifications/subscriptions/acknowledged if it agrees to honor it".
func TestListChanged_DeliveredOnListenWhenOptedIn(t *testing.T) {
	reg, c := listenStack(t)

	got := make(chan struct{}, 4)
	l, err := c.Listen(t.Context(), client.ListenOptions{
		Notifications: core.ListChangedFilter(ListChangedFilterKey),
		Handlers: map[string]func(json.RawMessage){
			"notifications/events/list_changed": func(json.RawMessage) { got <- struct{}{} },
		},
	})
	require.NoError(t, err)
	defer l.Stop()
	assert.True(t, l.Honored().Enabled(ListChangedFilterKey),
		"the acknowledgement must echo eventsListChanged; got %v", l.Honored())

	addFakeSource(t, reg, "fake.added")
	select {
	case <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("AddSource did not deliver notifications/events/list_changed on the opted-in listen stream")
	}
}

func TestListChanged_NotDeliveredWithoutOptIn(t *testing.T) {
	reg, c := listenStack(t)

	got := make(chan string, 4)
	l, err := c.Listen(t.Context(), client.ListenOptions{
		Notifications:  core.SubscriptionFilter{},
		OnNotification: func(method string, _ json.RawMessage) { got <- method },
	})
	require.NoError(t, err)
	defer l.Stop()
	assert.False(t, l.Honored().Enabled(ListChangedFilterKey))

	addFakeSource(t, reg, "fake.added")
	select {
	case m := <-got:
		t.Fatalf("a listen stream that did not opt in received %q", m)
	case <-time.After(300 * time.Millisecond):
	}
}
