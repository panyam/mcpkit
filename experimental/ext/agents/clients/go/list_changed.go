package agentsclient

import (
	"context"
	"encoding/json"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/agents"
)

// WatchListChanged opens a subscriptions/listen stream that asks for
// agentsListChanged and calls onChange each time
// notifications/agents/list_changed arrives. agents-wg#22 says a client
// receiving it SHOULD invalidate its cached roster and re-list before routing
// again, so passing a Cache's Invalidate as onChange does exactly that.
//
// It is a convenience for a host whose only listen stream is for agents. A
// host listening for several extensions should open one client.Listen with
// agents.ListChangedFilterKey in its filter instead, since a server keeps one
// stream per filter.
//
// Returns client.ErrListenNeedsStatelessWire on the legacy session wire,
// where the notification arrives on the GET stream through
// client.WithNotificationCallback. onChange runs on the stream's reader
// goroutine, so it must not block.
func WatchListChanged(ctx context.Context, c *client.Client, onChange func()) (*client.Listener, error) {
	return c.Listen(ctx, client.ListenOptions{
		Notifications: core.ListChangedFilter(agents.ListChangedFilterKey),
		Handlers: map[string]func(json.RawMessage){
			agents.NotificationListChanged: func(json.RawMessage) { onChange() },
		},
	})
}
