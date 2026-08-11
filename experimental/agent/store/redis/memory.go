package redisstore

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/panyam/mcpkit/experimental/agent"
)

// DefaultMemoryKeyPrefix namespaces every key the MemoryStore writes, kept
// distinct from the RunStore and ToolResultStore prefixes so all three can
// share one Redis without collision.
const DefaultMemoryKeyPrefix = "mcpkit.agent.memory"

// MemoryStore is the durable Redis backend for agent.MemoryStore: the plain
// substring working-memory store (the sibling of agent.InMemoryMemoryStore),
// made durable. Each namespace (a session id, a user id, the default "") is one
// Redis hash — "<prefix>:<namespace>" — with field=key, value=JSON(MemoryItem),
// so recall never crosses namespaces and one Redis holds many scratchpads.
//
// Recall is a substring match (case-insensitive, on key or value), scored 1 —
// the same "how" as InMemoryMemoryStore. Durable semantic recall (ANN) is the
// gorm pgvector store's job; a Redis vector backend is a separate follow-up.
//
// Listing order is by MemoryItem.CreatedAt then key, which an update preserves
// (Put keeps an existing note's CreatedAt), so the scratchpad does not reorder
// on edit — the same stable-ordering contract as the in-memory stores.
type MemoryStore struct {
	client *redis.Client
	prefix string
	ttl    time.Duration
}

var _ agent.MemoryStore = (*MemoryStore)(nil)

// MemoryOption customizes a MemoryStore. Distinct from the RunStore and
// ToolResultStore option types so the stores' options never mix.
type MemoryOption func(*MemoryStore)

// WithMemoryKeyPrefix overrides DefaultMemoryKeyPrefix.
func WithMemoryKeyPrefix(prefix string) MemoryOption {
	return func(s *MemoryStore) { s.prefix = prefix }
}

// WithMemoryTTL sets a per-namespace expiry: every Put (re)sets the namespace
// hash to live this long, so a session's scratchpad is evicted this long after
// its last write. Zero (the default) means no expiry — memory persists until
// the DB is cleared. Useful to garbage-collect abandoned session scratchpads on
// a shared Redis.
func WithMemoryTTL(ttl time.Duration) MemoryOption {
	return func(s *MemoryStore) { s.ttl = ttl }
}

// NewMemoryStore returns a store over the given client. The client is shared,
// not owned: Close it wherever it was constructed.
func NewMemoryStore(client *redis.Client, opts ...MemoryOption) *MemoryStore {
	s := &MemoryStore{client: client, prefix: DefaultMemoryKeyPrefix}
	for _, o := range opts {
		o(s)
	}
	return s
}

// key is the hash holding one namespace's notes.
func (s *MemoryStore) key(namespace string) string { return s.prefix + ":" + namespace }

// PutMemory upserts a note. A zero CreatedAt is stamped now on a first write;
// updating an existing key keeps its original CreatedAt, so the note's listing
// position is stable. With WithMemoryTTL set, the namespace's expiry is
// refreshed on every write.
func (s *MemoryStore) PutMemory(ctx context.Context, req agent.PutMemoryRequest) (agent.PutMemoryResponse, error) {
	item := req.Item
	hkey := s.key(req.Namespace)

	if item.CreatedAt.IsZero() {
		// Preserve an existing note's CreatedAt (stable order on update); else
		// stamp now.
		if prev, err := s.client.HGet(ctx, hkey, item.Key).Result(); err == nil {
			var existing agent.MemoryItem
			if json.Unmarshal([]byte(prev), &existing) == nil && !existing.CreatedAt.IsZero() {
				item.CreatedAt = existing.CreatedAt
			}
		}
		if item.CreatedAt.IsZero() {
			item.CreatedAt = time.Now().UTC()
		}
	}

	body, err := json.Marshal(item)
	if err != nil {
		return agent.PutMemoryResponse{}, fmt.Errorf("redisstore: encoding memory %q: %w", item.Key, err)
	}
	if err := s.client.HSet(ctx, hkey, item.Key, string(body)).Err(); err != nil {
		return agent.PutMemoryResponse{}, err
	}
	if s.ttl > 0 {
		_ = s.client.Expire(ctx, hkey, s.ttl).Err()
	}
	return agent.PutMemoryResponse{}, nil
}

// ListMemories returns the namespace's notes whose key or value contains
// req.Query (case-insensitive; all notes when Query is empty), oldest first,
// each scored 1, capped at req.Limit. It fetches the whole namespace hash and
// filters in-process — a working-memory scratchpad is small, and Redis has no
// server-side substring match over hash fields.
func (s *MemoryStore) ListMemories(ctx context.Context, req agent.ListMemoriesRequest) (agent.ListMemoriesResponse, error) {
	all, err := s.client.HGetAll(ctx, s.key(req.Namespace)).Result()
	if err != nil {
		return agent.ListMemoriesResponse{}, err
	}
	items := make([]agent.MemoryItem, 0, len(all))
	for _, body := range all {
		var it agent.MemoryItem
		if err := json.Unmarshal([]byte(body), &it); err != nil {
			return agent.ListMemoriesResponse{}, fmt.Errorf("redisstore: corrupt memory entry: %w", err)
		}
		items = append(items, it)
	}
	// HGetAll is unordered; restore the CreatedAt-then-key order the contract
	// promises (stable across updates).
	sort.Slice(items, func(i, j int) bool {
		if !items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].CreatedAt.Before(items[j].CreatedAt)
		}
		return items[i].Key < items[j].Key
	})

	q := strings.ToLower(req.Query)
	out := make([]agent.ScoredMemory, 0, len(items))
	for _, it := range items {
		if req.Limit > 0 && len(out) >= req.Limit {
			break
		}
		if q == "" || strings.Contains(strings.ToLower(it.Key), q) || strings.Contains(strings.ToLower(it.Value), q) {
			out = append(out, agent.ScoredMemory{Item: it, Score: 1})
		}
	}
	return agent.ListMemoriesResponse{Items: out}, nil
}

// DeleteMemory removes a note by key within its namespace. An unknown key is
// Deleted=false, not an error (the MemoryStore contract).
func (s *MemoryStore) DeleteMemory(ctx context.Context, req agent.DeleteMemoryRequest) (agent.DeleteMemoryResponse, error) {
	n, err := s.client.HDel(ctx, s.key(req.Namespace), req.Key).Result()
	if err != nil {
		return agent.DeleteMemoryResponse{}, err
	}
	return agent.DeleteMemoryResponse{Deleted: n > 0}, nil
}
