package events

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/panyam/mcpkit/core"
)

const (
	defaultYieldingMaxSize         = 1000
	defaultSubscriberBufferSize    = 64
)

// YieldingOption configures a YieldingSource at construction time.
type YieldingOption func(*yieldingConfig)

type yieldingConfig struct {
	maxSize         int
	cursorless      bool
	subscriberBuf   int
	bufferStore     EventBufferStore
}

// SubscriberEvent flows on the channel returned by YieldingSource.Subscribe.
// Discriminator semantics:
//
//   - Event delivery: Event populated; Error and Terminated nil.
//     Truncated=true signals "one or more events were dropped before this
//     one" — consumers SHOULD treat as a possible state gap and re-fetch
//     authoritative state if it matters. Per spec §"Push-Based Delivery"
//     → "Event Delivery" L285, the stream handler maps Truncated=true
//     onto a fresh notifications/events/active{truncated:true} that
//     precedes the event.
//
//   - Transient error: Error populated; Event/Terminated nil. Stream
//     subscribers map onto notifications/events/error per spec L255+L261;
//     stream stays open. Webhook delivery is unaffected (errors are
//     upstream, not delivery-side).
//
//   - Terminal: Terminated populated; Event/Error nil. Stream subscribers
//     map onto notifications/events/terminated per spec L783-795 and the
//     stream closes. Subscriber chans close after the terminal event.
//     One-shot — subsequent yields are no-ops.
//
// Riding Truncated on the next successful event (rather than a separate
// marker frame) keeps the channel order trivially correct under any
// buffer size and avoids the marker-starves-the-slot pathology when
// consumers stay behind.
type SubscriberEvent struct {
	Event      Event
	Truncated  bool
	Error      *EventDeliveryError
	Terminated *EventDeliveryError
}

// EventDeliveryError is the error payload carried in SubscriberEvent
// Error/Terminated variants and the stream notifications they map to
// (notifications/events/error, notifications/events/terminated).
// Mirrors a JSON-RPC error object shape — code + message — so receivers
// handle it consistently with how they handle other JSON-RPC failures.
type EventDeliveryError struct {
	Code    int
	Message string
}

// subscriberSlot is one registered Subscribe channel. pendingTruncated is
// set when a yield is dropped because the chan is full; the next successful
// send delivers a Truncated marker before the event itself.
//
// principal / subscriptionID / arguments: per-subscriber identity
// captured at Subscribe time so the per-yield fanout can build a
// HookContext + apply the EventDef's Match / Transform per subscriber
// per spec §"Server SDK Guidance" L623-629.
type subscriberSlot struct {
	ch               chan SubscriberEvent
	principal        string
	subscriptionID   string
	arguments        map[string]any
	pendingTruncated atomic.Bool
	// closeOnce gates the chan close so YieldTerminated and the
	// Subscribe cleanup goroutine can both attempt close without
	// racing into "close of closed channel".
	closeOnce sync.Once
}

// SubscribeOpts bundles the per-subscriber metadata Subscribe needs.
// A struct rather than a positional list so future additions (MaxAge,
// per-subscriber backpressure tuning) extend without rippling through
// every caller.
type SubscribeOpts struct {
	// Principal is the resolved subscription principal. Surfaced on
	// HookContext.Principal() during fanout so author Match /
	// Transform can do principal-aware filtering.
	Principal string
	// SubscriptionID is the server-derived sub_<base64> handle
	// (per-stream for push). Surfaced on HookContext.SubscriptionID()
	// during fanout. Empty for callers that don't have one.
	SubscriptionID string
	// Arguments is the subscribe-time parameter map. Surfaced as the
	// `arguments` argument to Match / Transform so authors can implement
	// "deliver only when arguments.severity matches event.data.severity"
	// per spec L633-644.
	//
	// Renamed from Params to track spec PR1 commit 082166f0 (inner
	// subscription field → tools/call shape).
	Arguments map[string]any
}

// closeChan idempotently closes the slot's channel. Safe to call from
// either the YieldTerminated fanout (under s.mu write lock) or the
// Subscribe cleanup goroutine (also under s.mu write lock).
func (s *subscriberSlot) closeChan() {
	s.closeOnce.Do(func() { close(s.ch) })
}

// deliverEvent sends one event to the slot's channel non-blocking,
// honoring the existing pendingTruncated bookkeeping. Used by both
// the per-yield fanout (after match/transform) and the targeted-
// deliver path used by EmitToSubscription (spec §"Server SDK
// Guidance" L630), which bypasses match/transform.
//
// Tolerates a closed-channel race: if Subscribe's cleanup goroutine
// has raced ahead and close()-d the channel between our read of
// the slot and our send, the recovered panic is treated as a normal
// drop. The slot is being torn down anyway.
func (s *subscriberSlot) deliverEvent(event Event) {
	defer func() { _ = recover() }()
	truncated := s.pendingTruncated.Load()
	select {
	case s.ch <- SubscriberEvent{Event: event, Truncated: truncated}:
		if truncated {
			s.pendingTruncated.Store(false)
		}
	default:
		s.pendingTruncated.Store(true)
	}
}

// WithSubscriberBuffer overrides the per-Subscribe channel buffer size
// (default 64). Larger buffers tolerate slower consumers without dropping;
// smaller buffers fail fast and surface gaps via Truncated markers earlier.
// Has no effect on existing subscribers.
func WithSubscriberBuffer(n int) YieldingOption {
	return func(c *yieldingConfig) {
		if n > 0 {
			c.subscriberBuf = n
		}
	}
}

// WithMaxSize caps the YieldingSource's internal ring buffer. Older events
// are evicted FIFO once the cap is reached. Default is 1000. Pass <=0 to
// keep the default. Has no effect when WithoutCursors is set (cursorless
// sources do not buffer events).
func WithMaxSize(n int) YieldingOption {
	return func(c *yieldingConfig) {
		if n > 0 {
			c.maxSize = n
		}
	}
}

// WithEventBufferStore plugs an external EventBufferStore in as the
// poll-buffer backend for this YieldingSource. When set, yield
// Append's each event to the store AND keeps the local in-memory
// ring for ByCursor/Recent backwards-compat; Poll reads from the
// store so subscribers polling across multi-replica deployments see
// consistent results. Default (no option) = legacy in-memory ring
// only. See issue 727.
func WithEventBufferStore(s EventBufferStore) YieldingOption {
	return func(c *yieldingConfig) {
		if s != nil {
			c.bufferStore = s
		}
	}
}

// WithoutCursors marks the source as cursorless: events are emitted with
// `cursor: null` on the wire, the source does not buffer events, and
// events/poll always returns empty. Use for ephemeral-state sources where
// replay carries no value (typing indicators, presence, current readings).
//
// Push and webhook fanout still work exactly the same — the difference is
// that subscribers can't replay missed events. The EventDef advertised by
// events/list carries `cursorless: true` so clients can plan accordingly.
func WithoutCursors() YieldingOption {
	return func(c *yieldingConfig) {
		c.cursorless = true
	}
}

// NewYieldingSource returns a push-style EventSource and a yield function.
// Call yield(data) to publish an event — the library handles cursor
// assignment, in-memory retention, and fanout to push + webhook subscribers
// (the latter once the source is passed to Register).
//
// The returned *YieldingSource[Data] keeps the typed payload alongside the
// wire-format Event in a single ring buffer. Callers that need typed access
// to recent events (e.g., MCP resource handlers) can use Recent / ByCursor
// to read without re-unmarshaling.
//
// Use this when the source pushes events at the library (bot callback, HTTP
// handler, channel reader). Use TypedSource instead when the source owns its
// storage and prefers to be called via Poll.
//
// Example:
//
//	source, yield := events.NewYieldingSource[AlertData](events.EventDef{
//	    Name:        "alert.fired",
//	    Description: "Fires when a new alert is triggered",
//	    Delivery:    []string{"push", "poll", "webhook"},
//	})
//
//	events.Register(events.Config{
//	    Sources:  []events.EventSource{source},
//	    Webhooks: webhooks,
//	    Server:   srv,
//	})
//
//	go alertWatcher(func(ctx context.Context, a AlertData) { _ = yield(ctx, a) })
//
// The returned yield closure takes a ctx so the W3C trace context
// carried on it (`core.TraceContext`) is stamped onto `event.Meta`
// at yield time. Cross-process consumers (webhook delivery,
// HTTPSource inject) read the persistent traceparent from `Meta`;
// in-process consumers (the emit hook, subscribers) get ctx
// directly. Caller-set `event.Meta.traceparent` via `SetMetaFunc`
// is preserved — the auto-stamp is a fallback. See SEP-414 P6
// (issue 683).
func NewYieldingSource[Data any](def EventDef, opts ...YieldingOption) (*YieldingSource[Data], func(context.Context, Data) error) {
	cfg := &yieldingConfig{maxSize: defaultYieldingMaxSize, subscriberBuf: defaultSubscriberBufferSize}
	for _, o := range opts {
		o(cfg)
	}
	if def.PayloadSchema == nil {
		def.PayloadSchema = core.GenerateSchema[Data]()
	}
	// Reflect cursorlessness onto the EventDef so events/list advertises it.
	if cfg.cursorless {
		def.Cursorless = true
	}

	s := &YieldingSource[Data]{
		def:           def,
		maxSize:       cfg.maxSize,
		cursorless:    cfg.cursorless,
		subscriberBuf: cfg.subscriberBuf,
		bufferStore:   cfg.bufferStore,
		tp:            core.NoopTracerProvider{},
	}
	yield := func(ctx context.Context, data Data) error {
		return s.yield(ctx, data)
	}
	return s, yield
}

// emitterAware is implemented by EventSources that want the library to
// install a fanout hook (push + webhook). Register type-asserts this and
// wires the hook. EventSources that don't implement it (e.g., TypedSource)
// are responsible for their own fanout via Emit / EmitToWebhooks.
//
// The hook signature takes ctx so the W3C trace context carried by
// the yield call flows through to the Emitter (and onward to the
// outbound webhook delivery's HTTP `traceparent` header) without
// going through an Event.Meta round-trip on every hop.
type emitterAware interface {
	SetEmitHook(func(context.Context, Event))
}

// yieldedEntry holds the typed payload alongside its wire-format Event.
// One marshal happens per yield; reads via Recent/ByCursor return the typed
// payload directly with no further unmarshal.
type yieldedEntry[Data any] struct {
	data  Data
	event Event
}

// YieldingSource is a push-style EventSource. It owns an in-memory ring
// buffer of typed payloads + wire Events; events/poll reads through the same
// buffer. Construct via NewYieldingSource.
//
// When constructed with WithoutCursors, the source skips buffering entirely.
// Push and webhook fanout still fire (events emitted with `cursor: null`),
// but Poll always returns empty and Recent / ByCursor return zero results.
type YieldingSource[Data any] struct {
	def           EventDef
	maxSize       int
	cursorless    bool
	subscriberBuf int
	seq           atomic.Int64

	mu          sync.RWMutex
	entries     []yieldedEntry[Data]
	emitHook    func(context.Context, Event)
	metaFunc    func(context.Context, Data) map[string]any
	subscribers []*subscriberSlot
	terminated  bool // one-shot YieldTerminated has fired; subsequent yields are no-ops

	// bufferStore is the optional cross-replica buffer backend (issue
	// 727). When set, yield Append's events to it AND keeps the
	// local entries ring for ByCursor/Recent backwards compat. Poll
	// reads from the store so cross-replica reads stay consistent.
	// nil = legacy in-memory-only behavior; existing single-replica
	// adopters get this path with zero configuration change.
	bufferStore EventBufferStore

	// tp opts the source into SEP-414 P6 fanout instrumentation (issue
	// 724). When set, every yield wraps the per-subscriber fanout loop
	// in one `events.fanout` span carrying counts (subscribers.total,
	// delivered, dropped_by_match, transforms.applied) — one span per
	// event, regardless of subscriber count. Parented by the yield ctx
	// so the span stitches into the originating request trace when
	// yield runs inside a request handler. Defaulted to
	// core.NoopTracerProvider{} so call sites can unconditionally call
	// StartSpan without nil-checking. events.Register installs the
	// configured Config.TracerProvider via SetTracerProvider after
	// construction.
	tp core.TracerProvider
}

func (s *YieldingSource[Data]) Def() EventDef { return s.def }

// SetMetaFunc installs a per-event metadata mapper. When non-nil, the
// source calls it during yield and assigns the result to Event.Meta
// (spec follow-on commit d4faef9 2026-05-01: optional `_meta` on
// EventOccurrence). Returning nil from f produces no `_meta` key on
// the wire (omitempty). Pass nil to clear.
//
// Set on a separate method (not via NewYieldingSource opts) because
// the mapper is type-parameterized over Data, which doesn't compose
// cleanly with the option-function pattern.
func (s *YieldingSource[Data]) SetMetaFunc(f func(context.Context, Data) map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metaFunc = f
}

// SetTracerProvider installs the TracerProvider the source uses to emit
// the per-yield `events.fanout` span (issue 724). Called by
// events.Register when Config.TracerProvider is set, so user code
// rarely calls this directly — wire the TP on the Config struct
// instead. Nil tp resets to the Noop default; the unconfigured path
// stays zero-overhead.
//
// Safe to call concurrently with yield(); the field is mu-guarded and
// yield reads it under mu.Lock before snapshotting subscribers.
func (s *YieldingSource[Data]) SetTracerProvider(tp core.TracerProvider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if tp == nil {
		s.tp = core.NoopTracerProvider{}
		return
	}
	s.tp = tp
}

// YieldError emits a transient-failure signal to all live subscribers.
// Stream subscribers map this onto a notifications/events/error frame
// per spec §"Push-Based Delivery" L255+L261; the subscription stays
// open. Webhook delivery is unaffected (errors are upstream-side, not
// delivery-side).
//
// No-op after YieldTerminated has fired (one-shot terminal semantic).
// Returns nil; the signature mirrors yield/YieldTerminated for consistency
// even though there's no marshal-failure path.
func (s *YieldingSource[Data]) YieldError(err EventDeliveryError) error {
	s.mu.Lock()
	if s.terminated {
		s.mu.Unlock()
		return nil
	}
	se := SubscriberEvent{Error: &err}
	s.fanoutLocked(se)
	s.mu.Unlock()
	return nil
}

// YieldTerminated emits a terminal signal to all live subscribers and
// closes their channels. Stream subscribers map this onto a
// notifications/events/terminated frame per spec §"Authorization"
// L783-795 and the stream returns. After this call, the source is
// terminated: subsequent yield / YieldError / YieldTerminated calls
// are silently dropped, and Poll returns empty.
//
// One-shot. Receivers seeing the terminal signal SHOULD remove their
// local subscription state — there's no recovery path for the source
// itself; recovery requires re-subscribing.
func (s *YieldingSource[Data]) YieldTerminated(err EventDeliveryError) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminated {
		return nil
	}
	s.terminated = true

	// Fanout the terminal event to every subscriber, then close their
	// channels. Closing under the same lock that gates yield's send-to-
	// chan ensures no concurrent writer can panic on the closed chan.
	se := SubscriberEvent{Terminated: &err}
	for _, sub := range s.subscribers {
		select {
		case sub.ch <- se:
		default:
			// If the chan is full, the subscriber is too far behind to
			// see the terminal signal cleanly. Closing without it is
			// acceptable — the chan-close itself is also a terminal
			// signal (range loop exits, select-on-closed returns zero).
		}
		sub.closeChan()
	}
	s.subscribers = nil
	return nil
}

// Receive implements server.NotificationRelayReceiver — the
// routing seam Pattern B transports (redisstore.Bus and equivalents)
// call on every cross-replica event received by this replica. Routes
// to LocalDeliver after type-asserting params to Event; silently no-ops
// for any other method (a defensive guard so a misconfigured transport
// pumping unrelated notifications through doesn't panic).
//
// Adopters writing a custom receiver to look up sources by event name
// can wrap a Registry; this method assumes the receiver IS the source
// and the caller has already routed by event.Name.
func (s *YieldingSource[Data]) Receive(ctx context.Context, method string, params any) {
	if method != "notifications/events/event" {
		return
	}
	ev, ok := params.(Event)
	if !ok {
		return
	}
	s.LocalDeliver(ctx, ev)
}

// LocalDeliver runs the per-subscriber fanout loop for a fully-formed
// event without re-buffering, without firing the configured Emitter
// hook, and without going through the yield path's serial mutex hold.
//
// Use case: Pattern B Subscribers (Redis pubsub, future Kafka/NATS
// transports) call LocalDeliver on every received cross-replica event
// so each LOCAL subscriber slot's Match / Transform runs the same as
// for a same-replica yield. Without this path, cross-replica events
// would either skip Match/Transform (broadcast shortcut → tenant
// scoping leaks) or re-fire the Emitter hook (publish-loop).
//
// The event is taken as-is — cursor, EventID, timestamp, Meta all
// come from the originating replica's yield. Don't synthesize a new
// cursor here; cross-replica subscribers MUST see the same cursor a
// same-replica subscriber sees, otherwise events/poll resume math
// breaks.
//
// Concurrency: safe to call from any goroutine (e.g. the Pattern B
// Subscriber's receive goroutine). Snapshots the subscriber slice
// under s.mu so concurrent Subscribe / cleanup races don't observe
// closed channels.
func (s *YieldingSource[Data]) LocalDeliver(_ context.Context, event Event) {
	s.mu.Lock()
	subs := append([]*subscriberSlot(nil), s.subscribers...)
	matchFn := s.def.Match
	transformFn := s.def.Transform
	s.mu.Unlock()
	for _, sub := range subs {
		_, _ = s.deliverEventToSlot(sub, event, matchFn, transformFn)
	}
}

// fanoutLocked sends a SubscriberEvent to every live subscriber.
// Caller MUST hold s.mu (write lock) so the close-on-cleanup goroutine
// in Subscribe doesn't race with our sends.
func (s *YieldingSource[Data]) fanoutLocked(se SubscriberEvent) {
	for _, sub := range s.subscribers {
		select {
		case sub.ch <- se:
		default:
			// Drop policy applies to Error variants too — slow consumer
			// shouldn't back-pressure the source. Unlike event drops,
			// errors don't carry a recovery semantic; the consumer just
			// misses the error notification. Future events still get
			// the Truncated flag if any actual events were dropped.
		}
	}
}

// Subscribe registers a per-call live-event channel for push delivery
// (the foundation for events/stream, spec §"Push-Based Delivery"
// L223+). The returned channel receives a SubscriberEvent for every
// yield() until ctx is Done; on cancellation the slot is removed from
// the source and the channel is closed (range loops exit cleanly,
// select-on-closed returns the zero value).
//
// SubscribeOpts carries per-subscriber identity (Principal,
// SubscriptionID, Arguments) onto the slot so fanout can build a
// HookContext and apply the EventDef's Match / Transform (spec
// §"Server SDK Guidance" L623-629) per subscriber. Pass the zero
// value for hook-less callers; the safe wrappers tolerate empty
// fields.
//
// On a slow consumer (channel full), yield does NOT block — the event
// is dropped for that subscriber and a Truncated marker is sent on the
// next successful send. Stream handlers map Truncated=true onto a fresh
// notifications/events/active{truncated:true, cursor:source.Latest()}
// per spec §"Push-Based Delivery" → "Event Delivery" L285.
//
// Cursorless sources still buffer-and-fanout to subscribers (push
// delivery works fine without replay); only Poll returns empty on
// cursorless. Returns (chan, sender). The sender closure delivers a
// single event to THIS specific slot, bypassing the per-yield
// fanout's Match / Transform — used by EmitToSubscription (spec
// §"Server SDK Guidance" L630) so an author who has the sub id can
// route directly to one subscriber. Callers that only want broadcast
// delivery can ignore the second return.
func (s *YieldingSource[Data]) Subscribe(ctx context.Context, opts SubscribeOpts) (<-chan SubscriberEvent, func(Event)) {
	slot := &subscriberSlot{
		ch:             make(chan SubscriberEvent, s.subscriberBuf),
		principal:      opts.Principal,
		subscriptionID: opts.SubscriptionID,
		arguments:      opts.Arguments,
	}
	s.mu.Lock()
	s.subscribers = append(s.subscribers, slot)
	s.mu.Unlock()

	go func() {
		<-ctx.Done()
		s.mu.Lock()
		defer s.mu.Unlock()
		for i, x := range s.subscribers {
			if x == slot {
				s.subscribers = append(s.subscribers[:i], s.subscribers[i+1:]...)
				break
			}
		}
		// Close inside the same lock that the fanout in yield() takes —
		// guarantees no concurrent send attempts a write to the closed
		// chan. Idempotent so YieldTerminated's close + this cleanup
		// don't race into a double-close.
		slot.closeChan()
	}()

	sender := func(event Event) { slot.deliverEvent(event) }
	return slot.ch, sender
}

// SubscriberCount returns the number of live Subscribe channels. Test/
// telemetry helper; the count is a snapshot, callers MUST NOT race on it
// for correctness.
func (s *YieldingSource[Data]) SubscriberCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.subscribers)
}

// Poll implements EventSource. Returns events with cursor strictly greater
// than the requested cursor, up to limit. The Cursor field of PollResult is
// the cursor of the last delivered event (or the head if none) so the
// client's cursor advances even on empty polls.
//
// Cursorless sources always return empty events + empty cursor; the wire
// layer translates the empty cursor to JSON null.
func (s *YieldingSource[Data]) Poll(cursor string, limit int) PollResult {
	if s.cursorless {
		return PollResult{}
	}

	s.mu.RLock()
	terminated := s.terminated
	store := s.bufferStore
	s.mu.RUnlock()

	// Terminated source returns empty. Poll callers — including the
	// events/poll handler — should observe nothing-to-deliver rather
	// than the residual entries from before termination.
	if terminated {
		return PollResult{}
	}

	// Cross-replica path (issue 727): when a buffer store is wired
	// in, delegate Poll to it so multi-replica deployments answer
	// consistently. Ctx-free path — same fallback to context.Background
	// the rest of the EventSource interface relies on; the cross-process
	// trace context is already stamped on each event.Meta at yield time.
	if store != nil {
		resp, err := store.Poll(context.Background(), PollEventsRequest{
			SourceName: s.def.Name,
			Cursor:     cursor,
			Limit:      limit,
		})
		if err == nil {
			return PollResult{Events: resp.Events, Cursor: resp.NextCursor, Truncated: resp.Truncated}
		}
		// Store error: fall through to the in-memory ring rather than
		// returning an empty page. Belt-and-suspenders for transient
		// backend hiccups.
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	c, _ := strconv.ParseInt(cursor, 10, 64)

	gap := false
	if c > 0 && len(s.entries) > 0 {
		oldest, _ := strconv.ParseInt(s.entries[0].event.CursorStr(), 10, 64)
		if c < oldest {
			gap = true
		}
	}

	out := make([]Event, 0, limit)
	for _, e := range s.entries {
		ec, _ := strconv.ParseInt(e.event.CursorStr(), 10, 64)
		if ec > c {
			out = append(out, e.event)
			if len(out) >= limit {
				break
			}
		}
	}

	next := cursor
	if len(out) > 0 {
		next = out[len(out)-1].CursorStr()
	} else if len(s.entries) > 0 {
		next = s.entries[len(s.entries)-1].event.CursorStr()
	}
	return PollResult{Events: out, Cursor: next, Truncated: gap}
}

// Latest implements EventSource. Returns the cursor of the most recently
// yielded event, or "" when the source is empty or cursorless.
func (s *YieldingSource[Data]) Latest() string {
	if s.cursorless {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.entries) == 0 {
		return ""
	}
	return s.entries[len(s.entries)-1].event.CursorStr()
}

// Recent returns up to n most-recently-yielded payloads, oldest-first within
// the returned slice. Resource handlers and other typed consumers use this to
// read the source's tail without traversing the wire format.
func (s *YieldingSource[Data]) Recent(n int) []Data {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if n <= 0 {
		return nil
	}
	if n > len(s.entries) {
		n = len(s.entries)
	}
	out := make([]Data, n)
	for i, e := range s.entries[len(s.entries)-n:] {
		out[i] = e.data
	}
	return out
}

// ByCursor returns the typed payload for the event with the given cursor.
// Returns (zero, false) if the cursor is not present in the buffer (either
// never existed, was evicted, or the source is cursorless).
func (s *YieldingSource[Data]) ByCursor(cursor string) (Data, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.entries {
		if e.event.CursorStr() == cursor {
			return e.data, true
		}
	}
	var zero Data
	return zero, false
}

// Len returns the current number of buffered events.
func (s *YieldingSource[Data]) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}

// SetEmitHook is called by Register to install the push + webhook fanout
// hook. User code should not normally call this directly.
func (s *YieldingSource[Data]) SetEmitHook(hook func(context.Context, Event)) {
	s.mu.Lock()
	s.emitHook = hook
	s.mu.Unlock()
}

func (s *YieldingSource[Data]) yield(ctx context.Context, data Data) error {
	// One-shot terminated check. Sources that have signaled terminal
	// can't deliver new events to subscribers (chans are closed).
	// Returning nil rather than error so callers wrapping yield in
	// event-driven code paths don't have to special-case the
	// post-terminated lifetime.
	s.mu.RLock()
	terminated := s.terminated
	s.mu.RUnlock()
	if terminated {
		return nil
	}

	now := time.Now()
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("yield: marshal: %w", err)
	}

	seq := s.seq.Add(1)
	event := Event{
		EventID:   fmt.Sprintf("evt_%d", seq),
		Name:      s.def.Name,
		Timestamp: now.Format(time.RFC3339),
		Data:      raw,
	}
	if !s.cursorless {
		cursor := strconv.FormatInt(seq, 10)
		event.Cursor = &cursor
	}

	s.mu.Lock()
	if s.metaFunc != nil {
		if m := s.metaFunc(ctx, data); len(m) > 0 {
			event.Meta = m
		}
	}
	// SEP-414 P6 (issue 683): stamp the ctx's W3C trace context onto
	// event.Meta so cross-process consumers (webhook HTTP delivery,
	// HTTPSource inject) can read it from the event itself without
	// the hop-by-hop ctx chain. metaFunc-set traceparent wins —
	// matches the caller-preserves semantic used by
	// core.InjectTraceContextIntoParams (server outbound _meta) and
	// the TS-side Apps Bridge relay (PR 702).
	if tc := core.TraceContextFromContext(ctx); !tc.IsZero() {
		if event.Meta == nil {
			event.Meta = map[string]any{}
		}
		if _, has := event.Meta[core.MetaKeyTraceparent]; !has {
			core.InjectTraceContext(event.Meta, tc)
		}
	}
	if !s.cursorless {
		// Only buffer when cursored — cursorless sources don't support
		// poll-side replay, so retaining events would just waste memory.
		s.entries = append(s.entries, yieldedEntry[Data]{data: data, event: event})
		if len(s.entries) > s.maxSize {
			s.entries = s.entries[len(s.entries)-s.maxSize:]
		}
		// Mirror to the cross-replica buffer store (issue 727) when
		// configured. The dual-write is cheap for the in-memory case
		// and load-bearing for cross-replica deployments where
		// Poll() must answer consistently regardless of which
		// replica handles it. Errors are logged but don't fail the
		// yield — local subscribers still see the event via the
		// in-memory ring above.
		if s.bufferStore != nil {
			if _, err := s.bufferStore.Append(ctx, AppendEventRequest{
				SourceName: s.def.Name, Event: event,
			}); err != nil {
				// TODO: surface via a Logger option once we add one.
				// For now silent — yield should not block on store errors.
				_ = err
			}
		}
	}
	// Snapshot subscribers under the lock, then drop the lock before
	// invoking author hooks. Match / Transform (spec §"Server SDK
	// Guidance" L623-629) fire on the hot path — running them while
	// holding s.mu would serialize the whole source on a slow author
	// callback. The cleanup goroutine in Subscribe also takes s.mu
	// before close()-ing the chan, so snapshotting + sending later
	// means a closed-chan send is possible during a close-vs-send
	// race; subscriberSlot.deliverEvent owns the recover for that.
	subs := append([]*subscriberSlot(nil), s.subscribers...)
	hook := s.emitHook
	matchFn := s.def.Match
	transformFn := s.def.Transform
	tp := s.tp
	s.mu.Unlock()

	// SEP-414 P6 (issue 724): emit one `events.fanout` span per yield
	// when the source is opted into instrumentation AND has at least one
	// subscriber. Zero-subscriber emission is the dominant idle state
	// for sources that have no subscribers registered yet — skipping the
	// span there keeps Tempo from drowning in empty fanout spans every
	// feeder tick. Parented by the yield ctx so the span stitches into
	// the originating request trace (when present) or starts a fresh
	// trace (when yield runs from a background feeder).
	var fanoutSpan core.Span = noopFanoutSpan{}
	if len(subs) > 0 {
		if _, recording := tp.(core.NoopTracerProvider); !recording {
			_, fanoutSpan = tp.StartSpan(ctx, "events.fanout",
				core.Attribute{Key: "mcp.event.name", Value: event.Name},
				core.Attribute{Key: "mcp.event.id", Value: event.EventID},
			)
			defer fanoutSpan.End()
		}
	}

	var total, delivered, dropped, transformed int
	for _, sub := range subs {
		total++
		matched, transformedThis := s.deliverEventToSlot(sub, event, matchFn, transformFn)
		if !matched {
			dropped++
			continue
		}
		delivered++
		if transformedThis {
			transformed++
		}
	}
	fanoutSpan.SetAttribute("events.subscribers.total", strconv.Itoa(total))
	fanoutSpan.SetAttribute("events.subscribers.delivered", strconv.Itoa(delivered))
	fanoutSpan.SetAttribute("events.subscribers.dropped_by_match", strconv.Itoa(dropped))
	fanoutSpan.SetAttribute("events.transforms.applied", strconv.Itoa(transformed))

	if hook != nil {
		hook(ctx, event)
	}
	return nil
}

// noopFanoutSpan is the zero-overhead path when fanout instrumentation
// is off (no TracerProvider configured, or zero subscribers). Avoids
// the NoopTracerProvider.StartSpan call entirely so the dominant
// idle-source case takes the minimum number of branches.
type noopFanoutSpan struct{}

func (noopFanoutSpan) End()                     {}
func (noopFanoutSpan) SetAttribute(_, _ string) {}
func (noopFanoutSpan) RecordError(_ error)      {}
func (noopFanoutSpan) AddLink(_ core.Link)      {}

// deliverEventToSlot applies the EventDef's Match / Transform for one
// subscriber and sends the resulting event onto its channel. Extracted
// from yield() to keep the per-subscriber logic readable; tied to
// private subscriberSlot internals + yield's lock discipline so it's
// not reusable outside this file.
//
// Hot-path discipline:
//   - safeMatch / safeTransform are nil-tolerant + panic-recovering;
//     a buggy author hook can't take down the fanout.
//   - Match=false skips the subscriber outright (and skips Transform).
//   - Transform returning (event, false) is the passthrough fast
//     path — we send the unmodified event reference, no per-subscriber
//     allocation cost.
//   - subscriberSlot.deliverEvent owns the close-vs-send race recovery
//     and the non-blocking + drop-with-Truncated semantics; same
//     codepath as the targeted-deliver closure used by
//     EmitToSubscription (spec §"Server SDK Guidance" L630).
// deliverEventToSlot returns (matched, transformed) so yield()'s fanout
// span can stamp accurate counts. matched=false means Match returned
// false (the subscriber was filtered out before delivery); transformed
// signals safeTransform actually modified the event (the
// passthrough/no-op transform case returns false). Caller increments
// counters accordingly.
func (s *YieldingSource[Data]) deliverEventToSlot(sub *subscriberSlot, event Event, matchFn MatchFunc, transformFn TransformFunc) (matched, transformed bool) {
	hc := newHookContext(context.Background(), sub.principal, sub.subscriptionID, DeliveryModePush)
	if !safeMatch(matchFn, hc, event, sub.arguments) {
		return false, false
	}
	delivered, modified := safeTransform(transformFn, hc, event, sub.arguments)
	sub.deliverEvent(delivered)
	return true, modified
}
