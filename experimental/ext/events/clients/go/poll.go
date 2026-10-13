package eventsclient

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/experimental/ext/events"
)

// DefaultPollFloor is the shortest wait between polls when the server asks
// for less, per spec §"Poll" ("Clients SHOULD apply a configurable floor
// (default 1000 ms)").
const DefaultPollFloor = time.Second

// PollOptions configures a poll loop. One loop serves one subscription; the
// spec requires a loop per subscription.
type PollOptions struct {
	// EventName is the source name on the server (e.g., "discord.message").
	EventName string

	// Arguments is the per-subscription parameter bag, as for Stream.
	Arguments map[string]any

	// Cursor is the resume point for the first poll. nil = "from now".
	Cursor *string

	// MaxAge is the replay floor sent on every poll (`maxAgeMs`). Zero means
	// no floor.
	MaxAge time.Duration

	// MaxEvents caps each batch (`maxEvents`). Zero leaves it to the server.
	MaxEvents int

	// Floor is the minimum wait between polls, applied to nextPollMs so a
	// misbehaving server cannot drive a tight loop. Zero means
	// DefaultPollFloor. It does not apply while the server reports hasMore,
	// which the spec says to drain immediately.
	Floor time.Duration

	// OnEvent fires for every event in every batch, in order.
	OnEvent func(events.Event)

	// OnTruncated fires when a poll result carries `truncated: true`, with the
	// fresh cursor the loop has already adopted. Events may have been
	// skipped; callers that care SHOULD re-fetch authoritative state.
	OnTruncated func(cursor *string)

	// OnListChanged fires when notifications/events/list_changed arrives on
	// a poll response. The spec says the client SHOULD re-call events/list;
	// the SDK holds no registry, so that is the caller's to do.
	OnListChanged func()
}

// PollCall is a running poll loop.
type PollCall struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    atomic.Pointer[error]

	mu     sync.RWMutex
	cursor *string
}

type pollResult struct {
	Events     []events.Event `json:"events"`
	Cursor     *string        `json:"cursor"`
	HasMore    bool           `json:"hasMore"`
	Truncated  bool           `json:"truncated"`
	NextPollMs *int           `json:"nextPollMs"`
}

// Poll runs an events/poll loop until Stop, parent cancellation, or a poll
// error. It returns once the first poll has answered, so a rejection
// (-32011 NotFound, -32012 Forbidden, -32014 Unsupported) surfaces here
// rather than inside the loop.
//
// Cursor handling follows §"Cursor Lifecycle": the loop persists every
// string cursor the server returns, including the fresh one that comes with
// `truncated: true`, and treats `cursor: null` or an absent `cursor` as
// "nothing to persist", polling with null from then on. It never invents a
// cursor from an eventId.
//
// A poll error after the first ends the loop; Err reports it. Retrying is the
// caller's call, since a NotFound means the event type is gone.
func Poll(parent context.Context, sess *client.Client, opts PollOptions) (*PollCall, error) {
	if opts.EventName == "" {
		return nil, errors.New("eventsclient: PollOptions.EventName is required")
	}
	if opts.Floor <= 0 {
		opts.Floor = DefaultPollFloor
	}

	ctx, cancel := context.WithCancel(parent)
	p := &PollCall{cancel: cancel, done: make(chan struct{}), cursor: opts.Cursor}

	first, err := p.pollOnce(ctx, sess, opts)
	if err != nil {
		cancel()
		close(p.done)
		return nil, err
	}

	safeGo("eventsclient.poll", func() {
		defer close(p.done)
		res := first
		for {
			if !res.HasMore {
				wait := opts.Floor
				if res.NextPollMs != nil {
					wait = max(time.Duration(*res.NextPollMs)*time.Millisecond, opts.Floor)
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(wait):
				}
			}
			if ctx.Err() != nil {
				return
			}
			next, err := p.pollOnce(ctx, sess, opts)
			if err != nil {
				if ctx.Err() == nil {
					p.err.Store(&err)
				}
				return
			}
			res = next
		}
	})
	return p, nil
}

// pollOnce makes one events/poll call, dispatches its events and adopts its
// cursor.
func (p *PollCall) pollOnce(ctx context.Context, sess *client.Client, opts PollOptions) (pollResult, error) {
	params := map[string]any{"name": opts.EventName, "cursor": p.Cursor()}
	if opts.MaxAge > 0 {
		params["maxAgeMs"] = opts.MaxAge.Milliseconds()
	}
	if opts.MaxEvents > 0 {
		params["maxEvents"] = opts.MaxEvents
	}
	if len(opts.Arguments) > 0 {
		params["arguments"] = opts.Arguments
	}

	cc := client.NewCallContext(ctx).WithNotifyHook(func(method string, _ json.RawMessage) {
		if method == "notifications/events/list_changed" && opts.OnListChanged != nil {
			opts.OnListChanged()
		}
	})
	raw, err := sess.CallContext(ctx, cc, "events/poll", params)
	if err != nil {
		return pollResult{}, err
	}
	var res pollResult
	if err := json.Unmarshal(raw.Raw, &res); err != nil {
		return pollResult{}, err
	}

	p.mu.Lock()
	p.cursor = res.Cursor
	p.mu.Unlock()

	for _, ev := range res.Events {
		if opts.OnEvent != nil {
			opts.OnEvent(ev)
		}
	}
	if res.Truncated && opts.OnTruncated != nil {
		opts.OnTruncated(res.Cursor)
	}
	return res, nil
}

// Cursor returns the cursor the next poll will send. Nil means "from now",
// which is also what a cursorless source always leaves here.
func (p *PollCall) Cursor() *string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.cursor == nil {
		return nil
	}
	c := *p.cursor
	return &c
}

// Stop ends the loop. Safe to call more than once; wait on Done to know the
// goroutine has exited.
func (p *PollCall) Stop() { p.cancel() }

// Done closes when the loop goroutine has exited.
func (p *PollCall) Done() <-chan struct{} { return p.done }

// Err returns the poll error that ended the loop, or nil if it was stopped.
func (p *PollCall) Err() error {
	if e := p.err.Load(); e != nil {
		return *e
	}
	return nil
}
