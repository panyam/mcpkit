package eventsclient

import (
	"context"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/experimental/ext/events"
)

// ChanStreamOptions configures StreamChan.
type ChanStreamOptions struct {
	// EventName is the source to stream. Required.
	EventName string

	// Buffer sizes the delivery channel (default 64). When the consumer
	// falls behind and the buffer fills, events are dropped through
	// OnDrop rather than blocking: the SSE reader that produces them
	// must never stall on a slow consumer.
	Buffer int

	// OnDrop observes events discarded on a full buffer. Nil discards
	// silently.
	OnDrop func(events.Event)

	// Cursor and Arguments pass through to the underlying stream call.
	Cursor    *string
	Arguments map[string]any
}

// StreamChan opens an events/stream call and delivers occurrences on a
// channel instead of a callback: the mechanism half of event consumption,
// so hosts compose channels (fan-in, mappers, pipelines) rather than
// nesting callbacks. The channel closes when the stream ends (Stop, ctx
// cancellation, or server termination).
func StreamChan(ctx context.Context, sess *client.Client, opts ChanStreamOptions) (<-chan events.Event, *StreamCall, error) {
	buf := opts.Buffer
	if buf <= 0 {
		buf = 64
	}
	ch := make(chan events.Event, buf)
	call, err := Stream(ctx, sess, StreamOptions{
		EventName: opts.EventName,
		Cursor:    opts.Cursor,
		Arguments: opts.Arguments,
		OnEvent: func(ev events.Event) {
			select {
			case ch <- ev:
			default:
				if opts.OnDrop != nil {
					opts.OnDrop(ev)
				}
			}
		},
	})
	if err != nil {
		return nil, nil, err
	}
	go func() {
		<-call.Done()
		close(ch)
	}()
	return ch, call, nil
}
