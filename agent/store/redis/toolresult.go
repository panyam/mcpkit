package redisstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/core"
)

// DefaultToolResultKeyPrefix namespaces every key a ToolResultStore
// writes, kept distinct from the RunStore prefix so both can share one
// Redis without collision.
const DefaultToolResultKeyPrefix = "mcpkit.agent.toolresult"

// ToolResultStore is the durable Redis backend for agent.ToolResultStore:
// one key per ref (<prefix>:<ref>) holding the JSON-encoded
// core.ToolResult. Retention is native Redis TTL (WithToolResultTTL);
// the default is no expiry. Eviction is always safe because the
// interface's unknown-ref contract (Found=false) is what read_tool_result
// turns into a graceful "no longer available" answer.
type ToolResultStore struct {
	client *redis.Client
	prefix string
	ttl    time.Duration
}

var _ agent.ToolResultStore = (*ToolResultStore)(nil)

// ToolResultOption customizes a ToolResultStore. Distinct from the
// RunStore Option type so the two stores' options never mix.
type ToolResultOption func(*ToolResultStore)

// WithToolResultKeyPrefix overrides DefaultToolResultKeyPrefix.
func WithToolResultKeyPrefix(prefix string) ToolResultOption {
	return func(s *ToolResultStore) { s.prefix = prefix }
}

// WithToolResultTTL sets a per-entry expiry: every stored result lives
// this long, then Redis evicts it and a later read degrades gracefully.
// Zero (the default) means no expiry — results persist until the DB is
// cleared. A session store on a shared Redis usually wants a TTL sized
// to how long a stub might be referenced.
func WithToolResultTTL(ttl time.Duration) ToolResultOption {
	return func(s *ToolResultStore) { s.ttl = ttl }
}

// NewToolResultStore returns a store over the given client. The client is
// shared, not owned: Close it wherever it was constructed.
func NewToolResultStore(client *redis.Client, opts ...ToolResultOption) *ToolResultStore {
	s := &ToolResultStore{client: client, prefix: DefaultToolResultKeyPrefix}
	for _, o := range opts {
		o(s)
	}
	return s
}

func (s *ToolResultStore) key(ref string) string { return s.prefix + ":" + ref }

// PutToolResult implements agent.ToolResultStore. Storing the same ref
// twice overwrites (and refreshes the TTL); callers never reuse a ref.
func (s *ToolResultStore) PutToolResult(ctx context.Context, req agent.PutToolResultRequest) (agent.PutToolResultResponse, error) {
	body, err := json.Marshal(req.Result)
	if err != nil {
		return agent.PutToolResultResponse{}, fmt.Errorf("redisstore: encoding tool result: %w", err)
	}
	if err := s.client.Set(ctx, s.key(req.Ref), body, s.ttl).Err(); err != nil {
		return agent.PutToolResultResponse{}, err
	}
	return agent.PutToolResultResponse{}, nil
}

// GetToolResult implements agent.ToolResultStore. A missing key (never
// stored, or TTL-evicted) is Found=false, not an error.
func (s *ToolResultStore) GetToolResult(ctx context.Context, req agent.GetToolResultRequest) (agent.GetToolResultResponse, error) {
	body, err := s.client.Get(ctx, s.key(req.Ref)).Result()
	if errors.Is(err, redis.Nil) {
		return agent.GetToolResultResponse{}, nil
	}
	if err != nil {
		return agent.GetToolResultResponse{}, err
	}
	var result core.ToolResult
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		return agent.GetToolResultResponse{}, fmt.Errorf("redisstore: corrupt tool result %q: %w", req.Ref, err)
	}
	return agent.GetToolResultResponse{Result: result, Found: true}, nil
}
