package host

import (
	"context"
	"fmt"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/experimental/ext/events"
	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
)

// subscription tracks one open event stream so the model (via meta-tools) or
// the user can list and cancel it.
type subscription struct {
	id     string
	server string
	name   string
	call   *eventsclient.StreamCall
}

// openSubscription opens an events/stream on the named server, tags its
// occurrences with the server id, and feeds them into the fan-in that drives
// the injection and trigger policies. Returns the subscription id (server:name)
// so the model can reference it for unsubscribe. Static config and the
// subscribe_events meta-tool share this one path.
func (a *App) openSubscription(ctx context.Context, serverID, name string) (string, error) {
	c := a.clientByID(serverID)
	if c == nil {
		return "", fmt.Errorf("unknown server %q", serverID)
	}
	id := serverID + ":" + name

	a.subsMu.Lock()
	if _, exists := a.subs[id]; exists {
		a.subsMu.Unlock()
		return id, nil // idempotent: already subscribed
	}
	a.subsMu.Unlock()

	ch, call, err := eventsclient.StreamChan(ctx, c, eventsclient.ChanStreamOptions{
		EventName: name,
		OnDrop:    func(ev events.Event) { a.emit(HostEvent{Kind: HostEventDropped, ServerID: serverID, EventName: name}) },
	})
	if err != nil {
		return "", err
	}

	a.subsMu.Lock()
	a.subs[id] = &subscription{id: id, server: serverID, name: name, call: call}
	a.subsMu.Unlock()

	adapted := make(chan agent.IncomingEvent, 16)
	go func() {
		defer close(adapted)
		for ev := range ch {
			adapted <- adaptEvent(serverID, ev)
		}
	}()
	a.fanIn.Add(adapted)
	return id, nil
}

// closeSubscription stops one stream. Unknown ids report false.
func (a *App) closeSubscription(id string) bool {
	a.subsMu.Lock()
	sub, ok := a.subs[id]
	if ok {
		delete(a.subs, id)
	}
	a.subsMu.Unlock()
	if !ok {
		return false
	}
	sub.call.Stop()
	return true
}

// listSubscriptions snapshots the open subscriptions.
func (a *App) listSubscriptions() []*subscription {
	a.subsMu.Lock()
	defer a.subsMu.Unlock()
	out := make([]*subscription, 0, len(a.subs))
	for _, s := range a.subs {
		out = append(out, s)
	}
	return out
}

// clientByID resolves a configured server id to its connected client, or nil.
func (a *App) clientByID(id string) *client.Client {
	for i, sc := range a.cfg.Servers {
		if sc.ID == id {
			return a.clients[i]
		}
	}
	return nil
}
