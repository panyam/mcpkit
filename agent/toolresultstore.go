package agent

import (
	"container/list"
	"context"
	"sync"

	"github.com/panyam/mcpkit/core"
)

// ToolResultStore is the persistence seam for offloaded tool results:
// full tool outputs an OffloadingSource stored out of band, keyed by a
// ref, so the conversation carries only a compact stub and the model
// fetches the detail on demand (read_tool_result). It is the "just in
// time context" primitive — lossless, pay-on-lookup — complementary to
// compaction, which is lossy and pays unconditionally.
//
// API shape follows the gRPC-style convention pinned in
// stores/STORAGE_SEAMS.md: Method(ctx, req) (resp, error), app-state on
// the response, error reserved for storage-layer faults. In particular
// an unknown ref is app-state (Found=false), never an error — a stub
// can legitimately outlive its blob once a backend evicts it, and
// read_tool_result turns Found=false into a graceful "no longer
// available" answer rather than a failure.
//
// The interface and its in-memory default live in agent/ because they
// traffic in core.ToolResult and pair with the agent-only
// OffloadingSource (A6). Durable backends are sibling modules
// (agent/store/redis, agent/store/gorm), where retention is a
// per-backend construction concern (native TTL, a GC sweep, an LRU cap)
// — never part of this contract, which is exactly why the read path is
// built to tolerate eviction.
type ToolResultStore interface {
	// PutToolResult stores a result and returns nothing beyond error.
	// Refs are caller-assigned (OffloadingSource mints them) and
	// treated as opaque; storing the same ref twice overwrites, but
	// callers never reuse a ref, so that path is not relied upon.
	PutToolResult(ctx context.Context, req PutToolResultRequest) (PutToolResultResponse, error)

	// GetToolResult fetches a result by ref. Found=false means the ref
	// is unknown (never stored, or evicted) — the caller's cue to
	// degrade gracefully, not an error.
	GetToolResult(ctx context.Context, req GetToolResultRequest) (GetToolResultResponse, error)
}

// PutToolResultRequest carries the ref and the full result to retain.
type PutToolResultRequest struct {
	Ref    string
	Result core.ToolResult
}

// PutToolResultResponse is empty today; it exists so the method can grow
// app-state (an assigned ref, a stored-bytes count) without a signature
// break, per the gRPC-style convention.
type PutToolResultResponse struct{}

// GetToolResultRequest fetches by ref.
type GetToolResultRequest struct {
	Ref string
}

// GetToolResultResponse carries the result when Found. The returned
// value is the caller's own copy; in-memory implementations return the
// stored value (results are treated as immutable once stored, so no
// clone is needed — OffloadingSource never mutates a stored result).
type GetToolResultResponse struct {
	Result core.ToolResult
	Found  bool
}

// InMemoryToolResultStore is the default ToolResultStore: a
// mutex-guarded map, safe for concurrent use. Nothing survives process
// exit — durable deployments swap in a sibling backend behind the same
// interface. An optional entry cap (WithMaxToolResults) evicts the
// least-recently-stored ref when exceeded; the graceful read contract
// makes that eviction safe. Default is unbounded.
type InMemoryToolResultStore struct {
	mu      sync.Mutex
	results map[string]core.ToolResult
	// order tracks insertion order for the LRU cap; front is oldest.
	// nil when maxEntries == 0 (unbounded, no bookkeeping).
	order      *list.List
	elems      map[string]*list.Element
	maxEntries int
}

// ToolResultStoreOption configures an InMemoryToolResultStore.
type ToolResultStoreOption func(*InMemoryToolResultStore)

// WithMaxToolResults caps the store at n stored results, evicting the
// oldest when a Put would exceed n. Zero or negative means unbounded
// (the default). Eviction is safe because read_tool_result degrades an
// unknown ref to a "no longer available" answer rather than an error.
func WithMaxToolResults(n int) ToolResultStoreOption {
	return func(s *InMemoryToolResultStore) {
		if n > 0 {
			s.maxEntries = n
			s.order = list.New()
			s.elems = map[string]*list.Element{}
		}
	}
}

// NewInMemoryToolResultStore returns an empty in-memory store.
func NewInMemoryToolResultStore(opts ...ToolResultStoreOption) *InMemoryToolResultStore {
	s := &InMemoryToolResultStore{results: map[string]core.ToolResult{}}
	for _, o := range opts {
		o(s)
	}
	return s
}

// PutToolResult implements ToolResultStore.
func (s *InMemoryToolResultStore) PutToolResult(ctx context.Context, req PutToolResultRequest) (PutToolResultResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.results[req.Ref]; !exists && s.order != nil {
		s.elems[req.Ref] = s.order.PushBack(req.Ref)
		for s.order.Len() > s.maxEntries {
			oldest := s.order.Front()
			ref := oldest.Value.(string)
			s.order.Remove(oldest)
			delete(s.elems, ref)
			delete(s.results, ref)
		}
	}
	s.results[req.Ref] = req.Result
	return PutToolResultResponse{}, nil
}

// GetToolResult implements ToolResultStore.
func (s *InMemoryToolResultStore) GetToolResult(ctx context.Context, req GetToolResultRequest) (GetToolResultResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, ok := s.results[req.Ref]
	if !ok {
		return GetToolResultResponse{}, nil
	}
	return GetToolResultResponse{Result: res, Found: true}, nil
}
