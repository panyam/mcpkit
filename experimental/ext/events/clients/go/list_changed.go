package eventsclient

import (
	"context"
	"encoding/json"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
)

// WatchListChanged opens a subscriptions/listen stream that asks for
// eventsListChanged and calls onChange each time
// notifications/events/list_changed arrives. SEP-3415 delivers that
// notification only on such a stream; the client SHOULD then call events/list
// again, which is the caller's to do since the SDK holds no registry.
//
// It is a convenience for a host whose only listen stream is for events. A host
// that listens for several extensions should open one client.Listen with
// events.ListChangedFilterKey in its filter instead, since a server keeps one
// stream per filter.
//
// Returns client.ErrListenNeedsStatelessWire on the legacy session wire,
// which has no subscriptions/listen. There the notification arrives on the GET
// stream, through client.WithNotificationCallback. onChange runs on the
// stream's reader goroutine, so it must not block.
func WatchListChanged(ctx context.Context, c *client.Client, onChange func()) (*client.Listener, error) {
	return c.Listen(ctx, client.ListenOptions{
		Notifications: core.ListChangedFilter(events.ListChangedFilterKey),
		Handlers: map[string]func(json.RawMessage){
			"notifications/events/list_changed": func(json.RawMessage) { onChange() },
		},
	})
}
