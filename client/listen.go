package client

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"

	core "github.com/panyam/mcpkit/core"
)

// subscriptions/listen on the 2026-07-28 stateless wire (SEP-2575).
//
// One Listener holds one open subscriptions/listen call. The request's
// params.notifications names what the caller wants, including keys that
// extensions define (eventsListChanged, agentsListChanged, taskIds). The
// server's acknowledgement says which of those it will honor, and every
// later notification on the stream is routed by method to a handler.
//
// A host that wants one stream per server builds the filter and the
// handler table from every extension it uses, then calls Listen once.

// ErrListenNeedsStatelessWire is returned by Listen on a client that
// connected over the legacy session wire. That wire has no
// subscriptions/listen; its notifications arrive on the GET stream and
// reach WithNotificationCallback instead.
var ErrListenNeedsStatelessWire = errors.New("subscriptions/listen needs the 2026-07-28 stateless wire")

// ListenOptions configures a Listen call.
type ListenOptions struct {
	// Notifications is the filter sent as params.notifications. Build it
	// with core.ListChangedFilter for boolean keys and Set for others.
	// Nil is sent as {}, which a server acknowledges with nothing honored.
	Notifications core.SubscriptionFilter

	// Handlers routes a notification to a function by its method. params
	// is the notification's params as received, _meta included.
	Handlers map[string]func(params json.RawMessage)

	// OnNotification receives any notification with no entry in Handlers.
	// Optional.
	OnNotification func(method string, params json.RawMessage)
}

// Listener is an open subscriptions/listen stream.
type Listener struct {
	subscriptionID json.RawMessage
	honored        core.SubscriptionFilter
	cancel         context.CancelFunc
	done           chan struct{}
	err            atomic.Pointer[error]
}

// Listen opens a subscriptions/listen stream and returns once the server
// has acknowledged it, or with the server's error if it refused (for
// example -32602 for a malformed filter, or
// core.ErrCodeSubscriptionLimitExceeded at the server's stream cap).
//
// Handlers run on the stream's reader goroutine, one at a time, in
// arrival order. They must not block, or later notifications wait behind
// them. Notifications on the stream also reach the client-wide callback
// set by WithNotificationCallback, as on any other stream.
//
// The stream lasts until Stop, until ctx is cancelled, or until the
// server closes it. Done is closed when it has ended, and Err then says
// whether it ended in an error.
func (c *Client) Listen(ctx context.Context, opts ListenOptions) (*Listener, error) {
	if !c.useStatelessWire {
		return nil, ErrListenNeedsStatelessWire
	}
	filter := opts.Notifications
	if filter == nil {
		filter = core.SubscriptionFilter{}
	}

	ctx, cancel := context.WithCancel(ctxOrBackground(ctx))
	l := &Listener{cancel: cancel, done: make(chan struct{})}

	acked := make(chan struct{})
	var ackSeen atomic.Bool
	hook := func(method string, params json.RawMessage) {
		if !ackSeen.Load() {
			if method != core.NotificationSubscriptionsAcknowledged {
				return
			}
			var ack struct {
				Meta          map[string]json.RawMessage `json:"_meta"`
				Notifications core.SubscriptionFilter    `json:"notifications"`
			}
			_ = json.Unmarshal(params, &ack)
			l.subscriptionID = ack.Meta[core.MetaKeySubscriptionID]
			l.honored = ack.Notifications
			if l.honored == nil {
				l.honored = core.SubscriptionFilter{}
			}
			ackSeen.Store(true)
			close(acked)
			return
		}
		if h, ok := opts.Handlers[method]; ok {
			h(params)
			return
		}
		if opts.OnNotification != nil {
			opts.OnNotification(method, params)
		}
	}

	callDone := make(chan error, 1)
	safeGo("client.listen", func() {
		defer close(l.done)
		cc := NewCallContext(ctx).WithNotifyHook(hook)
		_, err := c.CallContext(ctx, cc, core.MethodSubscriptionsListen, map[string]any{"notifications": filter})
		if err != nil && ctx.Err() == nil {
			l.err.Store(&err)
		}
		callDone <- err
	})

	select {
	case <-acked:
		return l, nil
	case err := <-callDone:
		cancel()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("subscriptions/listen ended before the server acknowledged it")
	case <-ctx.Done():
		cancel()
		return nil, ctx.Err()
	}
}

// SubscriptionID is the id the server stamps on this stream's
// notifications: the JSON-RPC id of the listen request, as raw JSON.
func (l *Listener) SubscriptionID() json.RawMessage { return l.subscriptionID }

// Honored is the filter the server agreed to, from its acknowledgement.
// A key the caller requested and the server declined or did not
// recognise is absent. Never nil.
func (l *Listener) Honored() core.SubscriptionFilter { return l.honored }

// Stop closes the stream. Safe to call more than once. Wait on Done to
// know the reader goroutine has exited.
func (l *Listener) Stop() { l.cancel() }

// Done is closed once the stream has ended and no handler will run again.
func (l *Listener) Done() <-chan struct{} { return l.done }

// Err reports why the stream ended, once Done is closed. It is nil for
// Stop, for cancellation of the ctx passed to Listen, and for a graceful
// close by the server.
func (l *Listener) Err() error {
	if p := l.err.Load(); p != nil {
		return *p
	}
	return nil
}
