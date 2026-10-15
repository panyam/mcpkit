package eventsclient_test

import (
	"errors"
	"net/http/httptest"
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

func listChangedStack(t *testing.T, mode client.ClientMode) (*events.Registry, *client.Client) {
	t.Helper()
	src, _ := events.NewYieldingSource[fakePayload](events.EventDef{Name: "fake.event", Description: "test", Delivery: []string{"poll"}})
	srv := server.NewServer(core.ServerInfo{Name: "watch-list-changed", Version: "0.1.0"})
	reg := events.Register(events.Config{
		Sources:                  []events.EventSource{src},
		Webhooks:                 events.NewWebhookRegistry(),
		Server:                   srv,
		UnsafeAnonymousPrincipal: "test-anon",
	})
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "t", Version: "1"}, client.WithClientMode(mode))
	require.NoError(t, c.Connect(t.Context()))
	t.Cleanup(func() { c.Close() })
	return reg, c
}

func TestWatchListChanged_FiresWhenTheCatalogChanges(t *testing.T) {
	reg, c := listChangedStack(t, client.ClientModeStateless)

	changed := make(chan struct{}, 4)
	l, err := eventsclient.WatchListChanged(t.Context(), c, func() { changed <- struct{}{} })
	require.NoError(t, err)
	defer l.Stop()

	require.NoError(t, reg.RemoveSource("fake.event"))
	select {
	case <-changed:
	case <-time.After(3 * time.Second):
		t.Fatal("RemoveSource did not reach WatchListChanged's callback")
	}
}

func TestWatchListChanged_NeedsTheStatelessWire(t *testing.T) {
	_, c := listChangedStack(t, client.ClientModeLegacyOnly)

	_, err := eventsclient.WatchListChanged(t.Context(), c, func() {})
	assert.True(t, errors.Is(err, client.ErrListenNeedsStatelessWire), "err = %v", err)
}
