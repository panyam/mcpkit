package eventsclient

// Stream — events/stream client helper (ε-4b).
//
// One Stream holds one open events/stream call. The helper:
//
//  1. Issues an events/stream POST via client.CallWithOptions, threading
//     a per-call notification hook (ε-4a) so notifications/events/* land
//     here rather than only on the session-global callback.
//  2. Routes each notification by method to the matching typed callback
//     (OnEvent, OnHeartbeat, OnTruncated, OnError, OnTerminated).
//  3. Returns from the constructor once the initial
//     notifications/events/active arrives — at that point the server has
//     accepted the subscription and the stream is live.
//  4. Surfaces server-side validation errors (-32011, -32012, -32017,
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
	// per spec §"Cursor Lifecycle" → "Bounding replay with maxAge" L529.
	// Zero means no floor. Resolution is seconds; sub-second precision
	// is dropped on the wire.
	MaxAge time.Duration

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
}

// StreamCall represents an open events/stream call.
type StreamCall struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    atomic.Pointer[error]
}

// Stream opens an events/stream call and dispatches incoming notifications
// to the typed callbacks in opts. Returns once the initial
// notifications/events/active arrives (signaling the stream is live) or
// the call fails immediately (e.g., -32011 EventNotFound, -32012
// Unauthorized, -32017 DeliveryModeUnsupported).
//
// The returned *StreamCall's Stop() cancels the underlying context, ending
// the stream — the server returns StreamEventsResult and the goroutine
// exits. Done() blocks until the goroutine has fully exited.
//
// Goroutine model: callbacks fire from the per-call notify hook (single
// goroutine per Stream — the SSE reader). They MUST NOT block, or the
// stream's heartbeat-timing reasoning falls apart. Push expensive work
// to a separate goroutine inside the callback.
func Stream(parent context.Context, sess *client.Client, opts StreamOptions) (*StreamCall, error) {
	if opts.EventName == "" {
		return nil, errors.New("eventsclient: StreamOptions.EventName is required")
	}

	ctx, cancel := context.WithCancel(parent)
	s := &StreamCall{cancel: cancel, done: make(chan struct{})}

	// activeReady fires on the first notifications/events/active arrival —
	// signals the constructor that the stream is live. Using sync.Once
	// here would be cleaner but atomic.Bool + close is faster and
	// avoids the once.Do allocation.
	activeReady := make(chan struct{})
	var activeFired atomic.Bool

	hook := func(method string, params json.RawMessage) {
		switch method {
		case "notifications/events/active":
			var p activeWire
			_ = json.Unmarshal(params, &p)
			if !activeFired.Swap(true) {
				close(activeReady)
				return
			}
			// Subsequent active = gap recovery (per spec L285).
			if p.Truncated && opts.OnTruncated != nil {
				opts.OnTruncated(p.Cursor)
			}

		case "notifications/events/event":
			if opts.OnEvent == nil {
				return
			}
			var ev events.Event
			if err := json.Unmarshal(params, &ev); err == nil {
				opts.OnEvent(ev)
			}

		case "notifications/events/heartbeat":
			if opts.OnHeartbeat == nil {
				return
			}
			var p heartbeatWire
			_ = json.Unmarshal(params, &p)
			opts.OnHeartbeat(p.Cursor)

		case "notifications/events/error":
			if opts.OnError == nil {
				return
			}
			var p errorWire
			if err := json.Unmarshal(params, &p); err == nil && p.Error != nil {
				opts.OnError(fmt.Errorf("RPC error %d: %s", p.Error.Code, p.Error.Message))
			}

		case "notifications/events/terminated":
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

	// Build params. Mirrors registerStream's request struct.
	params := map[string]any{"name": opts.EventName}
	if opts.Cursor != nil {
		params["cursor"] = *opts.Cursor
	}
	if opts.MaxAge > 0 {
		params["maxAge"] = int(opts.MaxAge / time.Second)
	}

	// Issue the call in a goroutine. CallContext blocks until the server
	// returns StreamEventsResult (after our cancel) or returns an
	// immediate error (e.g., validation failure).
	callDone := make(chan error, 1)
	go func() {
		defer close(s.done)
		cc := client.NewCallContext(ctx).WithNotifyHook(hook)
		_, err := sess.CallContext(cc, "events/stream", params)
		// Distinguish "we cancelled" from "server returned an error":
		// after cancel(), CallContext may return an error from the
		// transport (connection closed) — that's expected, not a failure.
		if err != nil && ctx.Err() == nil {
			ePtr := err
			s.err.Store(&ePtr)
		}
		callDone <- err
	}()

	// Wait for first active OR an early error from the call.
	select {
	case <-activeReady:
		return s, nil
	case err := <-callDone:
		// Call returned before any active arrived — server rejected the
		// subscription or the connection failed. Surface the error.
		if err != nil {
			return nil, err
		}
		return nil, errors.New("eventsclient: stream closed before notifications/events/active arrived")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Stop cancels the underlying call. Safe to call multiple times.
// After Stop returns, callers should wait on Done() to know when the
// stream goroutine has exited.
func (s *StreamCall) Stop() { s.cancel() }

// Done returns a channel closed when the stream goroutine has exited
// (the underlying CallWithOptions has returned).
func (s *StreamCall) Done() <-chan struct{} { return s.done }

// Err returns any non-cancellation error from the underlying call. Only
// meaningful after Done() is closed. Returns nil for clean shutdowns
// (Stop or context cancel).
func (s *StreamCall) Err() error {
	if p := s.err.Load(); p != nil {
		return *p
	}
	return nil
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
