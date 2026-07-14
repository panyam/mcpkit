package events

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
)

// EventBufferStore is the storage seam behind YieldingSource's poll
// buffer. The default in-memory implementation (InMemoryEventBufferStore
// in this file) matches the historical ring behavior bit-for-bit;
// alternative implementations plug in via WithEventBufferStore on
// YieldingSource so multi-replica deployments can share a common
// backend (e.g., Postgres-backed via stores/gorm) and answer Poll
// consistently regardless of which replica nginx routes the request
// to.
//
// Each event-source instance (`(YieldingSource[Data]).Def().Name`) is
// a logically independent buffer namespace. Implementations MUST
// partition state by the SourceName field — two YieldingSources with
// different Def().Name MUST NOT see each other's Append'd events.
//
// API shape: every method follows the gRPC-style
//
//	Method(ctx context.Context, req XRequest) (XResponse, error)
//
// convention pinned in STORAGE_SEAMS.md. ctx threads cancellation,
// deadlines, and trace context. Application-level state (Events,
// NextCursor, Truncated) lives on the response; error is reserved
// for storage-layer failures.
//
// Concurrency contract: YieldingSource takes its own write lock
// around Append calls, so a single-source implementation does NOT
// need internal locks against concurrent Appends from the same
// source. Implementations that share state across multiple sources
// (Postgres) or that allow concurrent Poll while Append runs MUST
// take their own locks.
type EventBufferStore interface {
	// Append records a single yielded event. Caller MUST set
	// AppendEventRequest.SourceName + Event.EventID + Event.Cursor.
	Append(ctx context.Context, req AppendEventRequest) (AppendEventResponse, error)

	// Poll returns up to Limit events from SourceName whose cursor is
	// strictly greater than Cursor. Cursor=="" means "start from now"
	// — return zero events + the source's current Latest cursor.
	//
	// Response.Truncated=true when the requested Cursor is older than
	// the oldest event the store still has (slice the client wanted
	// has been evicted). Clients re-subscribe from Latest. Per spec,
	// Truncated=true is the replay-failure signal.
	Poll(ctx context.Context, req PollEventsRequest) (PollEventsResponse, error)

	// Latest returns the cursor of the most recent Append'd event
	// for SourceName. Used by YieldingSource.Latest() to answer
	// `cursor: null` subscribe resolution. Empty when the source has
	// never been Append'd to (or has been fully evicted).
	Latest(ctx context.Context, req LatestCursorRequest) (LatestCursorResponse, error)

	// Recent returns the most recent N events for SourceName,
	// ordered oldest-first within the returned slice — same ordering
	// YieldingSource historically used.
	Recent(ctx context.Context, req RecentEventsRequest) (RecentEventsResponse, error)

	// Truncate drops events from SourceName whose Cursor is less
	// than or equal to BeforeCursor. Caller MAY pass BeforeCursor=""
	// to mean "drop everything for this source." Returns the number
	// of rows removed; implementations are free to approximate when
	// the underlying backend doesn't surface an exact count.
	//
	// Backends implement TTL eviction by periodically calling
	// Truncate with a BeforeCursor derived from "oldest event whose
	// expires_at < NOW()" — the store interface is intentionally
	// cursor-driven (not time-driven).
	Truncate(ctx context.Context, req TruncateEventsRequest) (TruncateEventsResponse, error)
}

// AppendEventRequest carries the event to record.
type AppendEventRequest struct {
	SourceName string
	Event      Event
}

// AppendEventResponse carries the cursor the store assigned to the event,
// when the store mints cursors on write (see CursorProvidingStore, issue
// 833). It is empty for stores that store the caller's cursor verbatim —
// the historical behavior — so existing implementations that return
// AppendEventResponse{} are unaffected.
type AppendEventResponse struct {
	// Cursor is the store-assigned cursor for the just-appended event.
	// Populated only when the store minted it (the caller passed a nil
	// Event.Cursor and the store provides cursors for the source);
	// empty when the caller supplied the cursor.
	Cursor string
}

// PollEventsRequest carries the (source, cursor, limit) tuple.
// Cursor=="" means "start from now". Limit<=0 is treated as 1 to
// preserve the existing YieldingSource contract that Poll always
// returns at least an empty page rather than an error.
type PollEventsRequest struct {
	SourceName string
	Cursor     string
	Limit      int
}

// PollEventsResponse carries the page of events + the next cursor
// the client should send on its next Poll. Truncated=true marks the
// requested cursor as too old (slice already evicted).
type PollEventsResponse struct {
	Events     []Event
	NextCursor string
	Truncated  bool
}

// LatestCursorRequest identifies a source. No paging.
type LatestCursorRequest struct {
	SourceName string
}

// LatestCursorResponse carries the source's most recent cursor.
// Cursor=="" when the source has never been Append'd to.
type LatestCursorResponse struct {
	Cursor string
}

// RecentEventsRequest asks for the most recent N events. Backends
// MAY return fewer when the buffer is smaller than N.
type RecentEventsRequest struct {
	SourceName string
	N          int
}

// RecentEventsResponse carries the most recent N events, ordered
// oldest-first within the returned slice.
type RecentEventsResponse struct {
	Events []Event
}

// TruncateEventsRequest carries the (source, beforeCursor) pair.
// BeforeCursor=="" drops the whole source's buffer.
type TruncateEventsRequest struct {
	SourceName   string
	BeforeCursor string
}

// TruncateEventsResponse reports how many events were dropped.
type TruncateEventsResponse struct {
	Removed int
}

// InMemoryEventBufferStore is the default implementation — a ring
// buffer per source name with a configurable max size. Matches the
// pre-seam YieldingSource behavior bit-for-bit: events accumulate up
// to MaxSize per source, oldest-first eviction past the cap,
// Truncated reported when the requested cursor is older than the
// surviving head.
//
// Thread-safe via an internal RWMutex. Used as the default when no
// WithEventBufferStore option is passed to NewYieldingSource — every
// existing adopter gets the historical in-memory behavior with zero
// configuration change.
type InMemoryEventBufferStore struct {
	mu      sync.RWMutex
	buffers map[string]*ringBuffer
	maxSize int

	// mintCursors opts the store into assigning cursors on write for a
	// nil Event.Cursor (CursorProvidingStore, issue 833). Off by default
	// so the store's historical verbatim-cursor behavior is unchanged.
	// When on, seq is the global monotone allocator; a source-partitioned
	// subset of a global monotone sequence is itself monotone, so
	// per-source poll-resume stays gap-free.
	mintCursors bool
	seq         atomic.Int64
}

type ringBuffer struct {
	events     []Event
	minCursor  string // tracks the eviction floor
	hasMinSeen bool
}

// NewInMemoryEventBufferStore returns an in-memory store with the
// given per-source max size. Max<=0 means "unbounded" — never evict.
// Cursors are stored verbatim (the caller's CursorProvider mints them).
func NewInMemoryEventBufferStore(maxSize int) *InMemoryEventBufferStore {
	return &InMemoryEventBufferStore{
		buffers: make(map[string]*ringBuffer),
		maxSize: maxSize,
	}
}

// NewCursorMintingInMemoryStore returns an in-memory store that assigns
// cursors on write from a shared global counter (CursorProvidingStore,
// issue 833). N YieldingSources for the same source that share one such
// store get globally-unique, monotone cursors — the multi-writer topology
// a shared Postgres buffer store provides in production, in a single
// process for tests and single-node fan-in deployments.
func NewCursorMintingInMemoryStore(maxSize int) *InMemoryEventBufferStore {
	s := NewInMemoryEventBufferStore(maxSize)
	s.mintCursors = true
	return s
}

// ProvidesCursor reports whether this store mints cursors on write. It is
// the CursorProvidingStore capability (issue 833); true only for a store
// built with NewCursorMintingInMemoryStore. The in-memory store either
// mints for every source or none, so source is ignored.
func (s *InMemoryEventBufferStore) ProvidesCursor(string) bool {
	return s.mintCursors
}

func (s *InMemoryEventBufferStore) ringFor(source string) *ringBuffer {
	r, ok := s.buffers[source]
	if !ok {
		r = &ringBuffer{}
		s.buffers[source] = r
	}
	return r
}

// Append records the event + evicts the head if the ring is past the
// configured max size. The dropped event's cursor (if any) becomes
// the new eviction floor for Truncated detection.
func (s *InMemoryEventBufferStore) Append(_ context.Context, req AppendEventRequest) (AppendEventResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ev := req.Event
	var assigned string
	if s.mintCursors && ev.Cursor == nil {
		// Assign from the shared global counter. A copy of the string is
		// taken so the stored event's Cursor pointer is independent.
		assigned = strconv.FormatInt(s.seq.Add(1), 10)
		c := assigned
		ev.Cursor = &c
	}
	r := s.ringFor(req.SourceName)
	r.events = append(r.events, ev)
	if s.maxSize > 0 && len(r.events) > s.maxSize {
		dropped := r.events[0]
		r.events = r.events[1:]
		if dropped.Cursor != nil {
			r.minCursor = *dropped.Cursor
			r.hasMinSeen = true
		}
	}
	return AppendEventResponse{Cursor: assigned}, nil
}

// Poll returns the slice of events whose cursor is strictly greater
// than req.Cursor. Reports Truncated=true when req.Cursor is older
// than the surviving head (slice the client wanted has been evicted).
func (s *InMemoryEventBufferStore) Poll(_ context.Context, req PollEventsRequest) (PollEventsResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r := s.ringFor(req.SourceName)
	limit := req.Limit
	if limit <= 0 {
		limit = 1
	}
	out := s.collectFrom(r, req.Cursor, limit)
	if req.Cursor != "" && r.hasMinSeen && cursorLessOrEqual(req.Cursor, r.minCursor) {
		out.Truncated = true
	}
	return out, nil
}

// collectFrom matches the historical YieldingSource.Poll semantics:
// empty cursor parses as 0 (return events from sequence 1+); for any
// cursor, append events whose cursor is strictly greater. NextCursor
// is the last appended event's cursor when we returned something, or
// Latest when nothing matched but the buffer has entries, or the
// caller's fromCursor unchanged when the buffer is empty.
func (s *InMemoryEventBufferStore) collectFrom(r *ringBuffer, fromCursor string, limit int) PollEventsResponse {
	out := PollEventsResponse{NextCursor: fromCursor}
	for _, e := range r.events {
		if e.Cursor == nil {
			continue
		}
		if fromCursor != "" && cursorLessOrEqual(*e.Cursor, fromCursor) {
			continue
		}
		out.Events = append(out.Events, e)
		out.NextCursor = *e.Cursor
		if len(out.Events) >= limit {
			break
		}
	}
	if len(out.Events) == 0 && len(r.events) > 0 {
		if c := r.events[len(r.events)-1].Cursor; c != nil {
			out.NextCursor = *c
		}
	}
	return out
}

// Latest returns the most recent event's cursor for the source, or
// "" when the source is empty.
func (s *InMemoryEventBufferStore) Latest(_ context.Context, req LatestCursorRequest) (LatestCursorResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r := s.ringFor(req.SourceName)
	if len(r.events) == 0 {
		return LatestCursorResponse{}, nil
	}
	c := r.events[len(r.events)-1].Cursor
	if c == nil {
		return LatestCursorResponse{}, nil
	}
	return LatestCursorResponse{Cursor: *c}, nil
}

// Recent returns the most recent N events for the source.
// Oldest-first within the returned slice.
func (s *InMemoryEventBufferStore) Recent(_ context.Context, req RecentEventsRequest) (RecentEventsResponse, error) {
	if req.N <= 0 {
		return RecentEventsResponse{}, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	r := s.ringFor(req.SourceName)
	if len(r.events) == 0 {
		return RecentEventsResponse{}, nil
	}
	start := len(r.events) - req.N
	if start < 0 {
		start = 0
	}
	out := make([]Event, len(r.events)-start)
	copy(out, r.events[start:])
	return RecentEventsResponse{Events: out}, nil
}

// Truncate drops events whose cursor is <= BeforeCursor.
// BeforeCursor=="" drops the whole source.
func (s *InMemoryEventBufferStore) Truncate(_ context.Context, req TruncateEventsRequest) (TruncateEventsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.ringFor(req.SourceName)
	if req.BeforeCursor == "" {
		removed := len(r.events)
		r.events = nil
		r.minCursor = ""
		r.hasMinSeen = false
		return TruncateEventsResponse{Removed: removed}, nil
	}
	removed := 0
	cut := 0
	for i, e := range r.events {
		if e.Cursor == nil {
			continue
		}
		if !cursorLessOrEqual(*e.Cursor, req.BeforeCursor) {
			cut = i
			break
		}
		removed++
		cut = i + 1
		r.minCursor = *e.Cursor
		r.hasMinSeen = true
	}
	r.events = r.events[cut:]
	return TruncateEventsResponse{Removed: removed}, nil
}

// cursorLessOrEqual compares YieldingSource-produced cursor strings
// — strconv.FormatInt(seq, 10) of a monotone int64. Compare
// numerically (not lex — "100" lex-< "9" would lie). Returns true
// iff a <= b numerically. Malformed cursors fall back to string
// compare (defensive — caller should never feed us non-numeric
// cursors from YieldingSource).
func cursorLessOrEqual(a, b string) bool {
	ai, aOK := parseCursor(a)
	bi, bOK := parseCursor(b)
	if aOK && bOK {
		return ai <= bi
	}
	return a <= b
}

func parseCursor(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int64(c-'0')
	}
	return n, true
}

// Compile-time checks.
var (
	_ EventBufferStore     = (*InMemoryEventBufferStore)(nil)
	_ CursorProvidingStore = (*InMemoryEventBufferStore)(nil)
)
