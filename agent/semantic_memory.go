package agent

import (
	"container/list"
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/panyam/mcpkit/core"
)

// SemanticMemoryStore is a MemoryStore that recalls by embedding similarity
// instead of substring match. It composes an Embedder (text -> vector) with
// an in-process brute-force cosine index: PutMemory embeds the note and keeps
// its vector; ListMemories embeds the query and returns items ranked by
// cosine similarity, each carrying its Score. It implements the same
// MemoryStore interface as the substring default, so swapping it in makes the
// recall tool (and the summary) semantic with no change to the model-facing
// surface — the "how" of retrieval stays behind the interface.
//
// The index is exact and O(n) per query, which is the right trade for a
// working-memory-sized scratchpad (tens to hundreds of notes). Approximate
// nearest-neighbor at scale is a durable-backend concern (a pgvector sibling
// MemoryStore), not something to build into the in-process default.
//
// Concurrency: safe for concurrent use. Embedding happens outside the lock
// (a network call for a hosted Embedder), so Put/List never hold the mutex
// across I/O.
type SemanticMemoryStore struct {
	embedder Embedder
	tp       core.TracerProvider

	mu    sync.Mutex
	items map[string]MemoryItem
	vecs  map[string][]float32
	// order tracks insertion order for the Query-empty listing and the cap,
	// mirroring InMemoryMemoryStore; front is oldest.
	order      *list.List
	elems      map[string]*list.Element
	maxEntries int
}

// SemanticMemoryOption configures a SemanticMemoryStore.
type SemanticMemoryOption func(*SemanticMemoryStore)

// WithSemanticMaxMemories caps the store at n items, evicting the oldest when
// a Put of a new key would exceed n. Zero or negative means unbounded.
func WithSemanticMaxMemories(n int) SemanticMemoryOption {
	return func(s *SemanticMemoryStore) {
		if n > 0 {
			s.maxEntries = n
		}
	}
}

// WithSemanticTracerProvider opts the store into an agent.memory.recall span
// per similarity query. Nil / NoopTracerProvider means zero overhead.
func WithSemanticTracerProvider(tp core.TracerProvider) SemanticMemoryOption {
	return func(s *SemanticMemoryStore) {
		if tp != nil {
			s.tp = tp
		}
	}
}

// NewSemanticMemoryStore builds a semantic store over embedder. The embedder
// is required; every note and query is embedded with it, so a store must be
// queried with the same Embedder it was built with.
func NewSemanticMemoryStore(embedder Embedder, opts ...SemanticMemoryOption) (*SemanticMemoryStore, error) {
	if embedder == nil {
		return nil, fmt.Errorf("agent: SemanticMemoryStore needs an Embedder")
	}
	s := &SemanticMemoryStore{
		embedder: embedder,
		tp:       core.NoopTracerProvider{},
		items:    map[string]MemoryItem{},
		vecs:     map[string][]float32{},
		order:    list.New(),
		elems:    map[string]*list.Element{},
	}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

// PutMemory embeds the note (key + value, so a recall query matches either)
// and upserts it. Embedding is synchronous — a just-remembered fact is
// immediately recallable; background/batch indexing of a distillation write
// path is a separate concern.
func (s *SemanticMemoryStore) PutMemory(ctx context.Context, req PutMemoryRequest) (PutMemoryResponse, error) {
	item := req.Item
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now()
	}
	vecs, err := s.embedder.Embed(ctx, []string{item.Key + " " + item.Value})
	if err != nil {
		return PutMemoryResponse{}, fmt.Errorf("agent: embedding memory %q: %w", item.Key, err)
	}
	var vec []float32
	if len(vecs) > 0 {
		vec = vecs[0]
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.items[item.Key]; !exists {
		s.elems[item.Key] = s.order.PushBack(item.Key)
		for s.maxEntries > 0 && s.order.Len() > s.maxEntries {
			oldest := s.order.Front()
			key := oldest.Value.(string)
			s.order.Remove(oldest)
			delete(s.elems, key)
			delete(s.items, key)
			delete(s.vecs, key)
		}
	}
	s.items[item.Key] = item
	s.vecs[item.Key] = vec
	return PutMemoryResponse{}, nil
}

// ListMemories ranks items by cosine similarity to the query. An empty Query
// returns all items oldest-first with Score 0 (the "list everything" path the
// summary uses — there is no query to score against). Limit caps the result.
func (s *SemanticMemoryStore) ListMemories(ctx context.Context, req ListMemoriesRequest) (ListMemoriesResponse, error) {
	if req.Query == "" {
		return s.listAll(req.Limit), nil
	}

	qvecs, err := s.embedder.Embed(ctx, []string{req.Query})
	if err != nil {
		return ListMemoriesResponse{}, fmt.Errorf("agent: embedding query: %w", err)
	}
	var qvec []float32
	if len(qvecs) > 0 {
		qvec = qvecs[0]
	}

	_, span := s.tp.StartSpan(ctx, "agent.memory.recall",
		core.Attribute{Key: "agent.memory.limit", Value: fmt.Sprint(req.Limit)})
	defer span.End()

	s.mu.Lock()
	scored := make([]ScoredMemory, 0, len(s.items))
	for e := s.order.Front(); e != nil; e = e.Next() {
		key := e.Value.(string)
		scored = append(scored, ScoredMemory{Item: s.items[key], Score: CosineSimilarity(qvec, s.vecs[key])})
	}
	s.mu.Unlock()

	sort.SliceStable(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })
	if req.Limit > 0 && len(scored) > req.Limit {
		scored = scored[:req.Limit]
	}
	span.SetAttribute("agent.memory.candidates", fmt.Sprint(len(scored)))
	if len(scored) > 0 {
		span.SetAttribute("agent.memory.top_score", fmt.Sprintf("%.4f", scored[0].Score))
	}
	return ListMemoriesResponse{Items: scored}, nil
}

func (s *SemanticMemoryStore) listAll(limit int) ListMemoriesResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ScoredMemory, 0, s.order.Len())
	for e := s.order.Front(); e != nil; e = e.Next() {
		if limit > 0 && len(out) >= limit {
			break
		}
		out = append(out, ScoredMemory{Item: s.items[e.Value.(string)]})
	}
	return ListMemoriesResponse{Items: out}
}

// DeleteMemory removes an item and its vector. An unknown key is
// Deleted=false, not an error (same contract as the substring store).
func (s *SemanticMemoryStore) DeleteMemory(ctx context.Context, req DeleteMemoryRequest) (DeleteMemoryResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[req.Key]; !ok {
		return DeleteMemoryResponse{Deleted: false}, nil
	}
	delete(s.items, req.Key)
	delete(s.vecs, req.Key)
	if e, ok := s.elems[req.Key]; ok {
		s.order.Remove(e)
		delete(s.elems, req.Key)
	}
	return DeleteMemoryResponse{Deleted: true}, nil
}
