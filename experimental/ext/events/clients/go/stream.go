package eventsclient

// Stream — events/stream client helper. SEP-3415 §"Push-Based Delivery"
// L223+.
//
// One Stream holds one events/stream subscription, reopening the call when it
// goes silent or drops (see Stream). The helper:
//
//  1. Issues an events/stream POST via client.CallWithOptions,
//     threading a per-call notification hook (CallContext.WithNotifyHook)
//     so notifications/events/* land here rather than only on the
//     session-global callback.
//  2. Routes each notification by method to the matching typed callback
//     (OnEvent, OnHeartbeat, OnTruncated, OnError, OnTerminated).
//  3. Returns from the constructor once the initial
//     notifications/events/active arrives — at that point the server has
//     accepted the subscription and the stream is live.
//  4. Surfaces server-side validation errors (-32023, -32024, -32026,
//     -32602) as constructor errors before the goroutine ever starts.
//
// Per-stream isolation comes from the per-call notify hook: each Stream
// has its own POST SSE response, so its hook only sees notifications for
// its own call. No global state, no requestId demuxing in the SDK.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/experimental/ext/events"
)

// StreamOptions configures a Stream call.
type StreamOptions struct {
	// EventName is the source name (e.g., "discord.message").
	EventName string

	// Cursor controls the resume point. nil = "from now" (server returns
	// its current head as the active cursor). Non-nil = explicit resume.
	Cursor *string

	// MaxAge is the per-stream replay floor sent on every events/stream
	// per SEP-3415 §"Cursor Lifecycle" (bounding replay with maxAgeMs).
	// Zero means no floor. Sent as the `maxAgeMs` integer; resolution is
	// milliseconds, and sub-millisecond precision is dropped on the wire.
	MaxAge time.Duration

	// Arguments is the per-subscription parameter bag the server's EventDef
	// hooks (Match / Transform / OnSubscribe / OnUnsubscribe) consume.
	// Nil or empty means "default match all, no transform, no per-sub
	// provisioning." See experimental/ext/events/hooks.go for the
	// HookContext shape.
	//
	// Renamed from Params to track spec PR1 commit 082166f0 (inner
	// subscription field → tools/call shape).
	Arguments map[string]any

	// DisableReconnect keeps Stream to a single events/stream call. By
	// default a stream that goes silent, or whose connection drops, is
	// reopened with its last cursor (see Stream).
	DisableReconnect bool

	// OnEvent fires for every notifications/events/event (the payload
	// frame). Receives the spec EventOccurrence shape (events.Event);
	// callers decode Data themselves via json.Unmarshal(ev.Data, &T).
	//
	// A typed Stream[Data] wrapper analogous to Receiver[Data] could be
	// added later if there's demand; the raw envelope keeps the v1 API
	// minimal.
	OnEvent func(events.Event)

	// OnHeartbeat fires for every notifications/events/heartbeat. Cursor
	// is the source's current head per spec L294 — nil for cursorless
	// sources. Heartbeat carries cursor so persisted cursors advance
	// during quiet periods.
	OnHeartbeat func(cursor *string)

	// OnTruncated fires when the server emits a fresh
	// notifications/events/active{truncated:true} mid-stream signaling
	// a gap (per spec L285). Cursor is the server's fresh resume point;
	// callers SHOULD persist it AND re-fetch authoritative state if they
	// care about the gap.
	OnTruncated func(cursor *string)

	// OnError fires for every notifications/events/error (transient
	// upstream failure; subscription stays active). Optional — most
	// SDKs simply log these.
	OnError func(error)

	// OnTerminated fires for notifications/events/terminated (subscription
	// has ended — auth revoked, etc.). The Stream's Done() channel closes
	// shortly after.
	OnTerminated func(error)

	// Dedup drops events whose eventId this stream, or anything sharing the
	// Deduper, has already delivered, including replays after a reconnect.
	// Nil means a private Deduper.
	Dedup *Deduper

	// AllowDuplicates turns dedup off, for callers who dedup themselves.
	AllowDuplicates bool
}

// StreamCall represents an open events/stream subscription. Unless
// StreamOptions.DisableReconnect is set it can span several events/stream
// calls: Stream reopens the call when it goes silent or its connection drops.
type StreamCall struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    atomic.Pointer[error]
}

// assumedHeartbeatInterval stands in for the server's heartbeat interval until
// two heartbeats have been seen. The server never announces its interval, and
// SEP-3415 caps it at 30 seconds, so before calibration a stream counts as dead
// after a full minute of silence.
const assumedHeartbeatInterval = 30 * time.Second

// Bounds of the backoff between reconnect attempts that fail at the transport.
const (
	reconnectBackoffMin = 250 * time.Millisecond
	reconnectBackoffMax = 30 * time.Second
)

// Stream opens an events/stream call and dispatches incoming notifications
// to the typed callbacks in opts. Returns once the initial
// notifications/events/active arrives (signaling the stream is live) or
// the call fails immediately (e.g., -32023 NotFound, -32024
// Forbidden, -32026 Unsupported).
//
// Reconnect: SEP-3415 says a client that has heard neither an event nor a
// heartbeat for more than twice the heartbeat interval SHOULD treat the
// stream as dead and reconnect with its cursor. The interval is never
// announced, so Stream measures it as the largest gap between consecutive
// heartbeats (assumedHeartbeatInterval until two have arrived). When the
// stream goes that quiet, or its connection drops without Stop being called,
// Stream opens a new events/stream call carrying the last cursor any frame
// delivered. A stream whose frames carried cursor:null reconnects with an
// explicit cursor:null rather than replaying from anything. Transport failures
// are retried with exponential backoff. A reconnect the server answers with a
// JSON-RPC error ends the stream with that error, since it is an answer and not
// an outage. notifications/events/terminated, a normal final result, and Stop
// all end the stream without reconnecting. Set
// StreamOptions.DisableReconnect for the single-call behavior.
//
// Callbacks carry on across reconnects. A reconnect's active frame with
// truncated:true fires OnTruncated, as gap recovery within one call does.
//
// The returned *StreamCall's Stop() ends the stream. Done() is closed once
// nothing more will be delivered, and Err() then reports why it ended, nil
// for Stop or a clean finish.
//
// Goroutine model: callbacks fire from the per-call notify hook (single
// goroutine per call, the SSE reader). They MUST NOT block, or the
// stream's heartbeat-timing reasoning falls apart. Push expensive work
// to a separate goroutine inside the callback.
func Stream(parent context.Context, sess *client.Client, opts StreamOptions) (*StreamCall, error) {
	if opts.EventName == "" {
		return nil, errors.New("eventsclient: StreamOptions.EventName is required")
	}

	ctx, cancel := context.WithCancel(parent)
	s := &StreamCall{cancel: cancel, done: make(chan struct{})}
	opts.Dedup = dedupFor(opts.Dedup, opts.AllowDuplicates)
	st := &streamState{opts: opts}

	first := st.open(ctx, sess, st.params(true))
	select {
	case <-first.active:
		safeGo("eventsclient.stream", func() { s.supervise(ctx, sess, st, first) })
		return s, nil
	case err := <-first.done:
		// The call returned before any active arrived: the server rejected
		// the subscription or the connection failed. Surface the error.
		cancel()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("eventsclient: stream closed before notifications/events/active arrived")
	case <-ctx.Done():
		first.cancel()
		<-first.done
		return nil, ctx.Err()
	}
}

// Stop cancels the stream. Safe to call multiple times.
// After Stop returns, callers should wait on Done() to know when the
// stream goroutine has exited.
func (s *StreamCall) Stop() { s.cancel() }

// Done returns a channel closed when the stream has ended and its
// goroutine has exited.
func (s *StreamCall) Done() <-chan struct{} { return s.done }

// Err returns why the stream ended. Only meaningful after Done() is
// closed. Returns nil for clean shutdowns (Stop, context cancel,
// notifications/events/terminated, or a normal final result), and the
// server's *client.RPCError when a reconnect was refused.
func (s *StreamCall) Err() error {
	if p := s.err.Load(); p != nil {
		return *p
	}
	return nil
}

func (s *StreamCall) setErr(err error) {
	e := err
	s.err.Store(&e)
}

// supervise watches the live call and reopens it until the stream ends.
func (s *StreamCall) supervise(ctx context.Context, sess *client.Client, st *streamState, cur *callAttempt) {
	defer close(s.done)
	defer s.cancel()
	for {
		err, dead := st.watch(ctx, cur)
		if ctx.Err() != nil {
			return
		}
		if !dead {
			var rpcErr *client.RPCError
			switch {
			case st.isTerminated():
				return
			case errors.As(err, &rpcErr):
				s.setErr(err)
				return
			case err == nil:
				// The server ended the stream with its final result.
				return
			}
			if st.opts.DisableReconnect {
				s.setErr(err)
				return
			}
		}
		next, err := st.reconnect(ctx, sess)
		if next == nil {
			if err != nil {
				s.setErr(err)
			}
			return
		}
		cur = next
	}
}

// callAttempt is one events/stream call within a stream.
type callAttempt struct {
	cancel context.CancelFunc
	done   chan error    // receives the call's result once it returns
	active chan struct{} // closed on this call's first notifications/events/active
}

// streamState is what the per-call notify hooks and the supervisor share.
type streamState struct {
	opts StreamOptions

	mu            sync.Mutex
	cursor        *string // last cursor any frame carried; nil means cursor:null
	cursorKnown   bool
	activeSeen    bool // the stream's first active has arrived
	terminated    bool
	lastFrame     time.Time
	lastHeartbeat time.Time // previous heartbeat in the current call
	maxGap        time.Duration
}

// params builds an events/stream request. The first call follows
// opts.Cursor. A reconnect sends the last cursor a frame delivered, including
// an explicit null for a cursorless stream.
func (st *streamState) params(initial bool) map[string]any {
	p := map[string]any{"name": st.opts.EventName}
	st.mu.Lock()
	known, cursor := st.cursorKnown, st.cursor
	st.mu.Unlock()
	switch {
	case !initial && known:
		if cursor == nil {
			p["cursor"] = nil
		} else {
			p["cursor"] = *cursor
		}
	case st.opts.Cursor != nil:
		p["cursor"] = *st.opts.Cursor
	}
	if st.opts.MaxAge > 0 {
		p["maxAgeMs"] = st.opts.MaxAge.Milliseconds()
	}
	if len(st.opts.Arguments) > 0 {
		p["arguments"] = st.opts.Arguments
	}
	return p
}

// open issues one events/stream call in its own goroutine.
func (st *streamState) open(ctx context.Context, sess *client.Client, params map[string]any) *callAttempt {
	actx, cancel := context.WithCancel(ctx)
	a := &callAttempt{cancel: cancel, done: make(chan error, 1), active: make(chan struct{})}
	st.mu.Lock()
	st.lastFrame = time.Now()
	st.lastHeartbeat = time.Time{}
	st.mu.Unlock()

	var activeOnce sync.Once
	hook := st.hook(func() { activeOnce.Do(func() { close(a.active) }) })
	safeGo("eventsclient.stream.call", func() {
		cc := client.NewCallContext(actx).WithNotifyHook(hook)
		_, err := sess.CallContext(actx, cc, "events/stream", params)
		a.done <- err
	})
	return a
}

// watch blocks until the call returns or, with reconnect enabled, the stream
// has been silent for longer than deadAfter. On ctx cancellation it cancels
// the call and waits for it.
func (st *streamState) watch(ctx context.Context, a *callAttempt) (err error, dead bool) {
	for {
		select {
		case err := <-a.done:
			return err, false
		case <-ctx.Done():
			a.cancel()
			<-a.done
			return ctx.Err(), false
		case <-time.After(st.checkEvery()):
			if !st.opts.DisableReconnect && st.silentFor() > st.deadAfter() {
				a.cancel()
				<-a.done
				return nil, true
			}
		}
	}
}

// reconnect opens new calls until one goes active. It gives up on ctx
// cancellation, on a termination, or when the server answers with a JSON-RPC
// error, which it returns.
func (st *streamState) reconnect(ctx context.Context, sess *client.Client) (*callAttempt, error) {
	backoff := reconnectBackoffMin
	for {
		a := st.open(ctx, sess, st.params(false))
		select {
		case <-a.active:
			return a, nil
		case err := <-a.done:
			if ctx.Err() != nil || st.isTerminated() {
				return nil, nil
			}
			var rpcErr *client.RPCError
			if errors.As(err, &rpcErr) {
				return nil, err
			}
		case <-time.After(st.deadAfter()):
			// Accepted the connection but never confirmed: treat as a failure.
			a.cancel()
			<-a.done
		case <-ctx.Done():
			a.cancel()
			<-a.done
			return nil, nil
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil, nil
		}
		backoff = min(backoff*2, reconnectBackoffMax)
	}
}

func (st *streamState) isTerminated() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.terminated
}

func (st *streamState) silentFor() time.Duration {
	st.mu.Lock()
	defer st.mu.Unlock()
	return time.Since(st.lastFrame)
}

// deadAfter is twice the calibrated heartbeat interval.
func (st *streamState) deadAfter() time.Duration {
	st.mu.Lock()
	gap := st.maxGap
	st.mu.Unlock()
	if gap <= 0 {
		gap = assumedHeartbeatInterval
	}
	return 2 * gap
}

func (st *streamState) checkEvery() time.Duration {
	return min(max(st.deadAfter()/4, 5*time.Millisecond), time.Second)
}

// frame records that a frame arrived, and the cursor it carried when it had
// one to report.
func (st *streamState) frame(heartbeat bool, cursor *string, hasCursor bool) {
	now := time.Now()
	st.mu.Lock()
	defer st.mu.Unlock()
	st.lastFrame = now
	if hasCursor {
		st.cursor, st.cursorKnown = cursor, true
	}
	if heartbeat {
		if !st.lastHeartbeat.IsZero() {
			st.maxGap = max(st.maxGap, now.Sub(st.lastHeartbeat))
		}
		st.lastHeartbeat = now
	}
}

// hook routes one call's notifications to the typed callbacks. onActive is
// that call's own first-active signal.
func (st *streamState) hook(onActive func()) func(string, json.RawMessage) {
	opts := st.opts
	return func(method string, params json.RawMessage) {
		switch method {
		case "notifications/events/active":
			var p activeWire
			_ = json.Unmarshal(params, &p)
			st.frame(false, p.Cursor, true)
			st.mu.Lock()
			first := !st.activeSeen
			st.activeSeen = true
			st.mu.Unlock()
			onActive()
			// Every active after the stream's first is gap recovery, within
			// one call (per spec L285) or on a reconnect.
			if !first && p.Truncated && opts.OnTruncated != nil {
				opts.OnTruncated(p.Cursor)
			}

		case "notifications/events/event":
			var ev events.Event
			if err := json.Unmarshal(params, &ev); err != nil {
				st.frame(false, nil, false)
				return
			}
			st.frame(false, ev.Cursor, true)
			if opts.Dedup != nil && opts.Dedup.Seen(ev.EventID) {
				return
			}
			if opts.OnEvent != nil {
				opts.OnEvent(ev)
			}

		case "notifications/events/heartbeat":
			var p heartbeatWire
			_ = json.Unmarshal(params, &p)
			st.frame(true, p.Cursor, true)
			if opts.OnHeartbeat != nil {
				opts.OnHeartbeat(p.Cursor)
			}

		case "notifications/events/error":
			st.frame(false, nil, false)
			if opts.OnError == nil {
				return
			}
			var p errorWire
			if err := json.Unmarshal(params, &p); err == nil && p.Error != nil {
				opts.OnError(fmt.Errorf("RPC error %d: %s", p.Error.Code, p.Error.Message))
			}

		case "notifications/events/terminated":
			st.mu.Lock()
			st.terminated = true
			st.mu.Unlock()
			if opts.OnTerminated == nil {
				return
			}
			var p errorWire
			_ = json.Unmarshal(params, &p)
			var e error
			if p.Error != nil {
				e = fmt.Errorf("RPC error %d: %s", p.Error.Code, p.Error.Message)
			}
			opts.OnTerminated(e)
		}
	}
}

// --- wire param shapes (mirrors stream.go server-side) ---

type activeWire struct {
	RequestID json.RawMessage `json:"requestId"`
	Cursor    *string         `json:"cursor"`
	Truncated bool            `json:"truncated,omitempty"`
}

type heartbeatWire struct {
	RequestID json.RawMessage `json:"requestId"`
	Cursor    *string         `json:"cursor"`
}

type errorWire struct {
	RequestID json.RawMessage `json:"requestId"`
	Error     *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}
