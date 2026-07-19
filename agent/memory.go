package agent

import (
	"container/list"
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/panyam/mcpkit/core"
)

// Reserved tool names MemorySource exposes so the model can manage its own
// working memory — a scratchpad it reads and writes across turns.
const (
	RememberToolName = "remember"
	RecallToolName   = "recall"
	ForgetToolName   = "forget"
)

// MemoryItem is one note in working memory: a short labeled value the model
// chose to keep. Key is the model-chosen (or auto-assigned) label used to
// recall or forget it; Value is the content; CreatedAt is when it was
// stored, used only for stable ordering in listings and the summary.
type MemoryItem struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	CreatedAt time.Time `json:"createdAt,omitzero"`
}

// MemoryStore is the persistence seam for working memory. It pairs with the
// agent-only MemorySource (the model-facing tools), so it lives in agent/
// and traffics in MemoryItem (A6 — same rationale as ToolResultStore and
// RunStore: a model-facing type keeps the seam out of root stores/).
//
// API shape follows the gRPC-style convention in stores/STORAGE_SEAMS.md:
// Method(ctx, req) (resp, error); app-state travels on the response, error
// is reserved for storage-layer faults. In particular, forgetting an
// unknown key is app-state (Deleted=false), never an error — the model may
// forget a key it never stored, and that is a normal "nothing to do"
// answer, not a failure.
//
// ListMemories carries an optional Query. The contract is loose on purpose:
// return the items relevant to Query, all items when Query is empty. The
// in-memory default interprets relevance as a substring match; a future
// semantic backend (issue 940) interprets the same Query as a similarity
// search without changing the tool surface the model sees.
type MemoryStore interface {
	// PutMemory upserts an item by Key. Storing an existing Key overwrites
	// its Value and keeps its original listing position, so an update does
	// not reorder the scratchpad.
	PutMemory(ctx context.Context, req PutMemoryRequest) (PutMemoryResponse, error)

	// ListMemories returns items relevant to req.Query (all items when
	// Query is empty), most-relevant first, capped at req.Limit. The "how"
	// of relevance is the implementation's business — the substring default
	// filters and returns Score 1, a semantic store ranks by embedding
	// similarity, a pgvector backend does ANN — but the contract (ranked,
	// scored, top-k) is the same, which is why recall never branches on the
	// backend.
	ListMemories(ctx context.Context, req ListMemoriesRequest) (ListMemoriesResponse, error)

	// DeleteMemory removes an item by Key. Deleted reports whether a
	// matching item existed; an unknown Key is Deleted=false, not an error.
	DeleteMemory(ctx context.Context, req DeleteMemoryRequest) (DeleteMemoryResponse, error)
}

// PutMemoryRequest carries the item to store. A zero CreatedAt is stamped
// with the store clock at Put time (caller-set wins, mirroring
// Message.Timestamp).
type PutMemoryRequest struct {
	Item MemoryItem
}

// PutMemoryResponse is empty today; it exists so the method can grow
// app-state without a signature break, per the gRPC-style convention.
type PutMemoryResponse struct{}

// ListMemoriesRequest filters by Query (empty means all) and caps the result
// at Limit (0 means no cap). Limit is the k of a top-k recall.
type ListMemoriesRequest struct {
	Query string
	Limit int
}

// ListMemoriesResponse carries the matching items, most-relevant first.
type ListMemoriesResponse struct {
	Items []ScoredMemory
}

// ScoredMemory pairs a stored MemoryItem with its per-query relevance Score.
// Score is a property of the QUERY, not of the stored fact (the same item
// scores differently against different queries), which is why it lives here
// on the result and not on MemoryItem — that keeps MemoryItem the durable,
// query-independent fact. A substring store returns 1 for a match; a
// semantic store returns cosine similarity. Room to grow: a future
// ranking/reranker stage can add per-signal provenance (similarity vs
// recency vs importance) here without touching the store or the item.
type ScoredMemory struct {
	Item  MemoryItem
	Score float64
}

// DeleteMemoryRequest identifies the item to forget by Key.
type DeleteMemoryRequest struct {
	Key string
}

// DeleteMemoryResponse reports whether an item was actually removed.
type DeleteMemoryResponse struct {
	Deleted bool
}

// InMemoryMemoryStore is the default MemoryStore: a mutex-guarded map with
// stable insertion ordering, safe for concurrent use. Nothing survives
// process exit — a durable, session-scoped backend is a sibling-module
// follow-up (mirroring the ToolResultStore redis/gorm arc). An optional
// entry cap (WithMaxMemories) evicts the oldest item when exceeded.
type InMemoryMemoryStore struct {
	mu    sync.Mutex
	items map[string]MemoryItem
	// order tracks insertion order so listings and the summary are
	// deterministic regardless of CreatedAt tie-breaks; front is oldest.
	order      *list.List
	elems      map[string]*list.Element
	maxEntries int
}

// MemoryStoreOption configures an InMemoryMemoryStore.
type MemoryStoreOption func(*InMemoryMemoryStore)

// WithMaxMemories caps the store at n items, evicting the oldest when a Put
// of a new key would exceed n. Zero or negative means unbounded (the
// default).
func WithMaxMemories(n int) MemoryStoreOption {
	return func(s *InMemoryMemoryStore) {
		if n > 0 {
			s.maxEntries = n
		}
	}
}

// NewInMemoryMemoryStore returns an empty in-memory store.
func NewInMemoryMemoryStore(opts ...MemoryStoreOption) *InMemoryMemoryStore {
	s := &InMemoryMemoryStore{
		items: map[string]MemoryItem{},
		order: list.New(),
		elems: map[string]*list.Element{},
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// PutMemory implements MemoryStore.
func (s *InMemoryMemoryStore) PutMemory(ctx context.Context, req PutMemoryRequest) (PutMemoryResponse, error) {
	item := req.Item
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now()
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
		}
	}
	s.items[item.Key] = item
	return PutMemoryResponse{}, nil
}

// ListMemories implements MemoryStore: substring match on key or value
// (case-insensitive), oldest first, each match scored 1 (the substring
// store has no graded relevance). Limit caps the count.
func (s *InMemoryMemoryStore) ListMemories(ctx context.Context, req ListMemoriesRequest) (ListMemoriesResponse, error) {
	q := strings.ToLower(req.Query)
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ScoredMemory
	for e := s.order.Front(); e != nil; e = e.Next() {
		if req.Limit > 0 && len(out) >= req.Limit {
			break
		}
		item := s.items[e.Value.(string)]
		if q == "" || strings.Contains(strings.ToLower(item.Key), q) || strings.Contains(strings.ToLower(item.Value), q) {
			out = append(out, ScoredMemory{Item: item, Score: 1})
		}
	}
	return ListMemoriesResponse{Items: out}, nil
}

// DeleteMemory implements MemoryStore.
func (s *InMemoryMemoryStore) DeleteMemory(ctx context.Context, req DeleteMemoryRequest) (DeleteMemoryResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[req.Key]; !ok {
		return DeleteMemoryResponse{Deleted: false}, nil
	}
	delete(s.items, req.Key)
	if e, ok := s.elems[req.Key]; ok {
		s.order.Remove(e)
		delete(s.elems, req.Key)
	}
	return DeleteMemoryResponse{Deleted: true}, nil
}

// MemorySource is working memory as a ToolSource: it exposes remember,
// recall, and forget tools over a MemoryStore, so the model manages its own
// scratchpad through ordinary tool calls. It is a leaf source (like
// FuncSource, not a wrapper like OffloadingSource) — a host adds it to its
// MultiSource alongside the server sources.
//
// Summary renders the current scratchpad for optional pre-turn injection,
// keeping the model aware of what it has stored without a recall call every
// turn. Injection is the host's job (through its existing EventInjectionPolicy
// path); MemorySource only supplies the text, so the Runner never changes.
type MemorySource struct {
	store MemoryStore
	fs    *FuncSource
}

type rememberArgs struct {
	// Key is the label to store under. Optional — a stable key lets you
	// update or forget the note later; if omitted, one is assigned.
	Key string `json:"key,omitempty"`
	// Value is the content to remember.
	Value string `json:"value"`
}

type recallArgs struct {
	// Query filters notes by substring of key or value. Omit to list all.
	Query string `json:"query,omitempty"`
}

type forgetArgs struct {
	// Key is the label of the note to delete.
	Key string `json:"key"`
}

// NewMemorySource builds a MemorySource over store and registers its three
// tools. The store is required.
func NewMemorySource(store MemoryStore) (*MemorySource, error) {
	m := &MemorySource{store: store, fs: NewFuncSource()}
	if err := AddFunc(m.fs, RememberToolName,
		"Save a note to your working memory so you can recall it on a later turn. Provide a short key to label it (optional; used to update or forget it later) and the value to store.",
		m.remember); err != nil {
		return nil, err
	}
	if err := AddFunc(m.fs, RecallToolName,
		"Read from your working memory. Optionally pass a query to filter notes by a substring of the key or value; omit it to list everything you have stored.",
		m.recall); err != nil {
		return nil, err
	}
	if err := AddFunc(m.fs, ForgetToolName,
		"Delete a note from your working memory by its key.",
		m.forget); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *MemorySource) remember(ctx context.Context, in rememberArgs) (string, error) {
	key := strings.TrimSpace(in.Key)
	if key == "" {
		key = "mem-" + randHex(4)
	}
	if _, err := m.store.PutMemory(ctx, PutMemoryRequest{Item: MemoryItem{Key: key, Value: in.Value}}); err != nil {
		return "", err
	}
	return "remembered: " + key, nil
}

func (m *MemorySource) recall(ctx context.Context, in recallArgs) (string, error) {
	resp, err := m.store.ListMemories(ctx, ListMemoriesRequest{Query: in.Query})
	if err != nil {
		return "", err
	}
	if len(resp.Items) == 0 {
		if in.Query == "" {
			return "working memory is empty", nil
		}
		return "no memories match " + in.Query, nil
	}
	return renderMemories(itemsOf(resp.Items)), nil
}

// itemsOf drops the per-query Score, yielding the bare stored items for
// rendering or recency budgeting.
func itemsOf(sms []ScoredMemory) []MemoryItem {
	items := make([]MemoryItem, len(sms))
	for i, sm := range sms {
		items[i] = sm.Item
	}
	return items
}

func (m *MemorySource) forget(ctx context.Context, in forgetArgs) (string, error) {
	resp, err := m.store.DeleteMemory(ctx, DeleteMemoryRequest{Key: in.Key})
	if err != nil {
		return "", err
	}
	if !resp.Deleted {
		return "no memory with key " + in.Key, nil
	}
	return "forgot: " + in.Key, nil
}

// Tools implements ToolSource by delegating to the internal FuncSource.
func (m *MemorySource) Tools(ctx context.Context) ([]core.ToolDef, error) {
	return m.fs.Tools(ctx)
}

// Call implements ToolSource by delegating to the internal FuncSource.
func (m *MemorySource) Call(ctx context.Context, name string, args map[string]any) (*core.ToolResult, error) {
	return m.fs.Call(ctx, name, args)
}

// SummaryOptions budgets what Summary renders, so injecting the scratchpad
// every turn stays bounded even if the store grows. Both limits prioritize
// by recency (newest notes win — they are the most likely to matter). The
// zero value is unbounded: the whole scratchpad, which is affordable while
// working memory is kept small (WithMaxMemories). Relevance-based selection
// (inject only what matches the current turn) is the semantic-recall upgrade,
// a separate seam.
type SummaryOptions struct {
	// MaxItems caps how many notes are rendered, keeping the newest. Zero
	// means no item cap.
	MaxItems int
	// MaxChars caps the rendered length of the notes (a cheap token proxy;
	// the fixed header is not counted), dropping the oldest kept notes until
	// they fit. Zero means no length cap.
	MaxChars int
}

// Summary renders the current working memory as a block a host can inject as
// a RoleSystem message before a turn, honoring opts as a recency-priority
// budget. It returns "" when memory is empty (or the budget admits nothing)
// so the host injects nothing.
func (m *MemorySource) Summary(ctx context.Context, opts SummaryOptions) (string, error) {
	resp, err := m.store.ListMemories(ctx, ListMemoriesRequest{})
	if err != nil {
		return "", err
	}
	items := budgetMemories(itemsOf(resp.Items), opts)
	if len(items) == 0 {
		return "", nil
	}
	return "Working memory (notes you saved earlier):\n" + renderMemories(items), nil
}

// DefaultRecallTopK bounds a relevance recall when RecallOptions.TopK is unset.
const DefaultRecallTopK = 5

// RecallOptions bounds a pre-turn relevance recall (RecallRelevant).
type RecallOptions struct {
	// TopK caps how many of the most-relevant notes are returned. Zero uses
	// DefaultRecallTopK.
	TopK int
	// MinScore drops notes scoring below it — the poison guard: with a
	// semantic store every note gets some cosine score, so without a floor a
	// low-TopK recall would inject the least-irrelevant notes even when
	// nothing is actually relevant. Zero means no floor (keep all TopK).
	MinScore float64
}

// RecallRelevant queries the store for notes relevant to query and renders
// them as a block a host can inject as a RoleSystem message before a turn.
// Unlike Summary (ambient, recency-budgeted, the whole scratchpad), this is
// targeted at the current turn: it surfaces what matters for what the user
// just said. The store's relevance ranking does the work (cosine for a
// semantic store, substring for the default), so this is backend-agnostic; it
// returns "" when the query is empty or nothing clears MinScore.
func (m *MemorySource) RecallRelevant(ctx context.Context, query string, opts RecallOptions) (string, error) {
	if strings.TrimSpace(query) == "" {
		return "", nil
	}
	topK := opts.TopK
	if topK <= 0 {
		topK = DefaultRecallTopK
	}
	resp, err := m.store.ListMemories(ctx, ListMemoriesRequest{Query: query, Limit: topK})
	if err != nil {
		return "", err
	}
	kept := make([]MemoryItem, 0, len(resp.Items))
	for _, sm := range resp.Items {
		if sm.Score < opts.MinScore {
			continue
		}
		kept = append(kept, sm.Item)
	}
	if len(kept) == 0 {
		return "", nil
	}
	return "Relevant to the current message (from your memory):\n" + renderMemories(kept), nil
}

// budgetMemories trims items (oldest-first) to opts, keeping the newest that
// fit. MaxItems keeps the last N; MaxChars then drops from the front (oldest)
// until the rendered length is within budget. The kept notes stay in
// chronological order for readability.
func budgetMemories(items []MemoryItem, opts SummaryOptions) []MemoryItem {
	if opts.MaxItems > 0 && len(items) > opts.MaxItems {
		items = items[len(items)-opts.MaxItems:]
	}
	if opts.MaxChars > 0 {
		for len(items) > 0 && len(renderMemories(items)) > opts.MaxChars {
			items = items[1:]
		}
	}
	return items
}

func renderMemories(items []MemoryItem) string {
	var b strings.Builder
	for i, it := range items {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("- ")
		b.WriteString(it.Key)
		b.WriteString(": ")
		b.WriteString(it.Value)
	}
	return b.String()
}

func randHex(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}
