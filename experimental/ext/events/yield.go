package events

import (
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/panyam/mcpkit/core"
)

const defaultYieldingMaxSize = 1000

// YieldingOption configures a YieldingSource at construction time.
type YieldingOption func(*yieldingConfig)

type yieldingConfig struct {
	maxSize int
}

// WithMaxSize caps the YieldingSource's internal ring buffer. Older events
// are evicted FIFO once the cap is reached. Default is 1000. Pass <=0 to
// keep the default.
func WithMaxSize(n int) YieldingOption {
	return func(c *yieldingConfig) {
		if n > 0 {
			c.maxSize = n
		}
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
//	go alertWatcher(func(a AlertData) { _ = yield(a) })
func NewYieldingSource[Data any](def EventDef, opts ...YieldingOption) (*YieldingSource[Data], func(Data) error) {
	cfg := &yieldingConfig{maxSize: defaultYieldingMaxSize}
	for _, o := range opts {
		o(cfg)
	}
	if def.PayloadSchema == nil {
		def.PayloadSchema = core.GenerateSchema[Data]()
	}

	s := &YieldingSource[Data]{def: def, maxSize: cfg.maxSize}
	yield := func(data Data) error {
		return s.yield(data)
	}
	return s, yield
}

// emitterAware is implemented by EventSources that want the library to
// install a fanout hook (push + webhook). Register type-asserts this and
// wires the hook. EventSources that don't implement it (e.g., TypedSource)
// are responsible for their own fanout via Emit / EmitToWebhooks.
type emitterAware interface {
	SetEmitHook(func(Event))
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
type YieldingSource[Data any] struct {
	def     EventDef
	maxSize int
	seq     atomic.Int64

	mu       sync.RWMutex
	entries  []yieldedEntry[Data]
	emitHook func(Event)
}

func (s *YieldingSource[Data]) Def() EventDef { return s.def }

// Poll implements EventSource. Returns events with cursor strictly greater
// than the requested cursor, up to limit. The Cursor field of PollResult is
// the cursor of the last delivered event (or the head if none) so the
// client's cursor advances even on empty polls.
func (s *YieldingSource[Data]) Poll(cursor string, limit int) PollResult {
	s.mu.RLock()
	defer s.mu.RUnlock()

	c, _ := strconv.ParseInt(cursor, 10, 64)

	gap := false
	if c > 0 && len(s.entries) > 0 {
		oldest, _ := strconv.ParseInt(s.entries[0].event.Cursor, 10, 64)
		if c < oldest {
			gap = true
		}
	}

	out := make([]Event, 0, limit)
	for _, e := range s.entries {
		ec, _ := strconv.ParseInt(e.event.Cursor, 10, 64)
		if ec > c {
			out = append(out, e.event)
			if len(out) >= limit {
				break
			}
		}
	}

	next := cursor
	if len(out) > 0 {
		next = out[len(out)-1].Cursor
	} else if len(s.entries) > 0 {
		next = s.entries[len(s.entries)-1].event.Cursor
	}
	return PollResult{Events: out, Cursor: next, CursorGap: gap}
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
// never existed or was evicted).
func (s *YieldingSource[Data]) ByCursor(cursor string) (Data, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.entries {
		if e.event.Cursor == cursor {
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
func (s *YieldingSource[Data]) SetEmitHook(hook func(Event)) {
	s.mu.Lock()
	s.emitHook = hook
	s.mu.Unlock()
}

func (s *YieldingSource[Data]) yield(data Data) error {
	now := time.Now()
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("yield: marshal: %w", err)
	}

	seq := s.seq.Add(1)
	cursor := strconv.FormatInt(seq, 10)
	event := Event{
		EventID:   fmt.Sprintf("evt_%d", seq),
		Name:      s.def.Name,
		Timestamp: now.Format(time.RFC3339),
		Data:      raw,
		Cursor:    cursor,
	}

	s.mu.Lock()
	s.entries = append(s.entries, yieldedEntry[Data]{data: data, event: event})
	if len(s.entries) > s.maxSize {
		s.entries = s.entries[len(s.entries)-s.maxSize:]
	}
	hook := s.emitHook
	s.mu.Unlock()

	if hook != nil {
		hook(event)
	}
	return nil
}
