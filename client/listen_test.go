package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

const (
	eventsListChanged = "notifications/events/list_changed"
	agentsListChanged = "notifications/agents/list_changed"
)

// newListenServer serves one tool, so tools.listChanged is advertised,
// plus two extension filter keys standing in for events and agents.
func newListenServer(t *testing.T, opts ...server.Option) (*server.Server, string) {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "listen-test", Version: "1"}, opts...)
	srv.RegisterTool(core.ToolDef{Name: "echo", InputSchema: map[string]any{"type": "object"}},
		func(core.ToolContext, core.ToolRequest) (core.ToolResponse, error) { return core.TextResult("ok"), nil })
	for key, method := range map[string]string{"eventsListChanged": eventsListChanged, "agentsListChanged": agentsListChanged} {
		if err := srv.RegisterSubscriptionFilter(server.ListChangedFilterKey(key, method)); err != nil {
			t.Fatal(err)
		}
	}
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)
	return srv, ts.URL + "/mcp"
}

func connect(t *testing.T, url string, mode client.ClientMode) *client.Client {
	t.Helper()
	c := client.NewClient(url, core.ClientInfo{Name: "listen-client", Version: "1"}, client.WithClientMode(mode))
	if err := c.Connect(t.Context()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func waitFor(t *testing.T, ch <-chan string, want string) {
	t.Helper()
	select {
	case got := <-ch:
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %q", want)
	}
}

// One stream carries the base key and two extension keys, and each
// notification reaches the handler registered for its method.
func TestListen_OneStreamDispatchesByMethod(t *testing.T) {
	srv, url := newListenServer(t)
	c := connect(t, url, client.ClientModeStateless)

	got := make(chan string, 8)
	l, err := c.Listen(t.Context(), client.ListenOptions{
		Notifications: core.ListChangedFilter("toolsListChanged", "eventsListChanged", "agentsListChanged", "unknownKey"),
		Handlers: map[string]func(json.RawMessage){
			"notifications/tools/list_changed": func(json.RawMessage) { got <- "tools" },
			eventsListChanged:                  func(json.RawMessage) { got <- "events" },
		},
		OnNotification: func(method string, _ json.RawMessage) { got <- "other:" + method },
	})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer l.Stop()

	want := core.ListChangedFilter("toolsListChanged", "eventsListChanged", "agentsListChanged")
	if g, _ := json.Marshal(l.Honored()); string(g) != string(mustJSON(want)) {
		t.Errorf("Honored = %s, want %s", g, mustJSON(want))
	}
	if len(l.SubscriptionID()) == 0 {
		t.Error("SubscriptionID is empty")
	}

	ctx := context.Background()
	srv.Broadcast(ctx, "notifications/tools/list_changed", nil)
	waitFor(t, got, "tools")
	srv.Broadcast(ctx, eventsListChanged, map[string]any{})
	waitFor(t, got, "events")
	srv.Broadcast(ctx, agentsListChanged, map[string]any{})
	waitFor(t, got, "other:"+agentsListChanged)
}

func TestListen_LegacyWireRefused(t *testing.T) {
	_, url := newListenServer(t)
	c := connect(t, url, client.ClientModeLegacyOnly)

	_, err := c.Listen(t.Context(), client.ListenOptions{Notifications: core.ListChangedFilter("toolsListChanged")})
	if !errors.Is(err, client.ErrListenNeedsStatelessWire) {
		t.Errorf("err = %v, want ErrListenNeedsStatelessWire", err)
	}
}

func TestListen_StopEndsCleanly(t *testing.T) {
	_, url := newListenServer(t)
	c := connect(t, url, client.ClientModeStateless)

	l, err := c.Listen(t.Context(), client.ListenOptions{Notifications: core.ListChangedFilter("toolsListChanged")})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	l.Stop()
	select {
	case <-l.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("Done not closed after Stop")
	}
	if l.Err() != nil {
		t.Errorf("Err after Stop = %v, want nil", l.Err())
	}
}

func TestListen_ServerRefusalIsReturned(t *testing.T) {
	_, url := newListenServer(t, server.WithStatelessSubscriptionCap(1))
	c := connect(t, url, client.ClientModeStateless)

	first, err := c.Listen(t.Context(), client.ListenOptions{Notifications: core.ListChangedFilter("toolsListChanged")})
	if err != nil {
		t.Fatalf("first Listen: %v", err)
	}
	defer first.Stop()

	_, err = c.Listen(t.Context(), client.ListenOptions{Notifications: core.ListChangedFilter("toolsListChanged")})
	var rpcErr *client.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != core.ErrCodeSubscriptionLimitExceeded {
		t.Errorf("second Listen err = %v, want RPCError %d", err, core.ErrCodeSubscriptionLimitExceeded)
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
