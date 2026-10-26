package eventsclient

import (
	"context"
	"errors"
	"fmt"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/experimental/ext/events"
)

// maxListPages bounds how many events/list pages List follows, so a server
// that never stops sending nextCursor is an error rather than a hang. It
// matches the client's WithMaxListPages default.
const maxListPages = 1000

// ErrListOverrun is returned by List when events/list kept sending a
// nextCursor past maxListPages. The definitions gathered before the bound are
// discarded: a truncated catalog that looks complete is worse than an error,
// because a host would treat it as everything the server offers.
var ErrListOverrun = errors.New("eventsclient: events/list exceeded the page bound")

// List returns every event type the server advertises through events/list,
// following nextCursor until the server stops sending one. Each EventDef
// carries what a host needs to decide what to watch, including its delivery
// modes, argument schema and _meta (such as a contextHint).
//
// A host that wants the list kept fresh calls List again when
// notifications/events/list_changed arrives (WatchListChanged on the stateless
// wire). The SDK holds no catalog of its own.
func List(ctx context.Context, c *client.Client) ([]events.EventDef, error) {
	var (
		all    []events.EventDef
		cursor string
	)
	for page := 0; page < maxListPages; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		res, err := c.Call(ctx, "events/list", params)
		if err != nil {
			return nil, fmt.Errorf("eventsclient: events/list: %w", err)
		}
		var lr struct {
			Events     []events.EventDef `json:"events"`
			NextCursor string            `json:"nextCursor"`
		}
		if err := res.Unmarshal(&lr); err != nil {
			return nil, fmt.Errorf("eventsclient: decode events/list: %w", err)
		}
		all = append(all, lr.Events...)
		if lr.NextCursor == "" {
			return all, nil
		}
		cursor = lr.NextCursor
	}
	return nil, fmt.Errorf("%w: %d pages", ErrListOverrun, maxListPages)
}
