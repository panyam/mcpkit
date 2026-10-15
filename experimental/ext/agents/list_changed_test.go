package agents_test

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/agents"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/server/stateless"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// listenForListChanged serves the agents extension on the stateless wire and
// opens a subscriptions/listen stream asking for agentsListChanged. Each
// notification puts one value on the returned channel.
func listenForListChanged(t *testing.T) (*agents.Registry, *client.Listener, <-chan struct{}) {
	t.Helper()
	srv := newCatalogServer()
	reg, err := agents.Register(agents.Config{Server: srv, Agents: []agents.AgentDef{sepWorkflowAgent()}})
	require.NoError(t, err)
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true), server.WithStatelessMode(stateless.ModeStateless)))
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "0.0.1"}, client.WithClientMode(client.ClientModeStateless))
	require.NoError(t, c.Connect(t.Context()))
	t.Cleanup(func() { c.Close() })

	got := make(chan struct{}, 8)
	l, err := c.Listen(t.Context(), client.ListenOptions{
		Notifications: core.ListChangedFilter(agents.ListChangedFilterKey),
		Handlers: map[string]func(json.RawMessage){
			agents.NotificationListChanged: func(json.RawMessage) { got <- struct{}{} },
		},
	})
	require.NoError(t, err)
	t.Cleanup(l.Stop)
	return reg, l, got
}

func expectNotifications(t *testing.T, got <-chan struct{}, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-got:
		case <-time.After(3 * time.Second):
			t.Fatalf("got %d list_changed notification(s), want %d", i, n)
		}
	}
	select {
	case <-got:
		t.Fatalf("got more than %d list_changed notification(s)", n)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestListChangedFilterKeyHonored(t *testing.T) {
	_, l, _ := listenForListChanged(t)
	assert.True(t, l.Honored().Enabled(agents.ListChangedFilterKey),
		"acknowledgement must echo agentsListChanged, got %v", l.Honored())
}

func TestAddAndRemoveAgentNotify(t *testing.T) {
	reg, _, got := listenForListChanged(t)

	require.NoError(t, reg.AddAgent(opsAgent()))
	expectNotifications(t, got, 1)

	reg.RemoveAgent(opsAgent().Name)
	expectNotifications(t, got, 1)

	reg.NotifyListChanged()
	expectNotifications(t, got, 1)
}

// A change that leaves the roster as it was must not make every listening
// host re-list.
func TestNoOpChangesDoNotNotify(t *testing.T) {
	reg, _, got := listenForListChanged(t)

	reg.RemoveAgent("no-such-agent")
	assert.Error(t, reg.AddAgent(sepWorkflowAgent()), "duplicate name")
	expectNotifications(t, got, 0)
}
