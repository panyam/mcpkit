// Package redisstore is the durable Redis backend for agent.RunStore.
//
// It is a sibling module (its own go.mod) so the go-redis dependency
// stays out of agent/, mirroring the root stores/ + stores/redis split.
// The wire format is encoding/json — agent messages and events are
// wire-serializable by constraint A2, so a Run round-trips Redis
// unchanged.
//
// Key layout, all under a configurable prefix (default
// DefaultKeyPrefix):
//
//	<prefix>:<runID>:meta      string — JSON {parentId, createdAt}
//	<prefix>:<runID>:messages  list   — one JSON agent.Message per entry
//	<prefix>:<runID>:events    list   — one JSON agent.Event per entry
//
// The meta key is the run's existence marker: CreateRun claims it with
// SETNX (collision detection without data loss), and every other method
// checks it before touching the lists.
package redisstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/panyam/mcpkit/agent"
)

// DefaultKeyPrefix namespaces every key this store writes. Deployments
// sharing a Redis with other mcpkit primitives (which use the
// "mcpkit."-prefixed channel namespace) stay collision-free by default.
const DefaultKeyPrefix = "mcpkit.agent.run"

// RunStore implements agent.RunStore on Redis. Safe for concurrent use;
// atomicity notes per method:
//
//   - CreateRun is atomic (SETNX on the meta key).
//   - AppendMessages / AppendEvents check existence then push without a
//     transaction: there is no delete API, so the window cannot orphan
//     data.
//   - ForkRun snapshots the source lists, then writes the copy. Appends
//     to the source that race the fork may or may not be included;
//     callers wanting an exact cut point should quiesce the source
//     first.
type RunStore struct {
	client *redis.Client
	prefix string
}

var _ agent.RunStore = (*RunStore)(nil)

// Option customizes a RunStore.
type Option func(*RunStore)

// WithKeyPrefix overrides DefaultKeyPrefix.
func WithKeyPrefix(prefix string) Option {
	return func(s *RunStore) { s.prefix = prefix }
}

// New returns a RunStore over the given client. The client is shared,
// not owned: Close it wherever it was constructed.
func New(client *redis.Client, opts ...Option) *RunStore {
	s := &RunStore{client: client, prefix: DefaultKeyPrefix}
	for _, o := range opts {
		o(s)
	}
	return s
}

// runMeta is the JSON body of the meta key.
type runMeta struct {
	ParentID  string    `json:"parentId,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

func (s *RunStore) key(id, part string) string {
	return s.prefix + ":" + id + ":" + part
}

// newRunID generates a collision-resistant random ID for runs created
// without a caller-chosen name. 8 random bytes (16 hex chars) is plenty
// for a per-deployment run namespace; CreateRun still SETNX-guards it.
func newRunID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("redisstore: generating run ID: %w", err)
	}
	return "run-" + hex.EncodeToString(b[:]), nil
}

// claimMeta writes the meta key iff absent. Returns whether the claim
// won.
func (s *RunStore) claimMeta(ctx context.Context, id string, meta runMeta) (bool, error) {
	body, err := json.Marshal(meta)
	if err != nil {
		return false, fmt.Errorf("redisstore: encoding run meta: %w", err)
	}
	return s.client.SetNX(ctx, s.key(id, "meta"), body, 0).Result()
}

// CreateRun implements agent.RunStore.
func (s *RunStore) CreateRun(ctx context.Context, req agent.CreateRunRequest) (agent.CreateRunResponse, error) {
	meta := runMeta{CreatedAt: time.Now().UTC()}
	if req.RunID != "" {
		won, err := s.claimMeta(ctx, req.RunID, meta)
		if err != nil {
			return agent.CreateRunResponse{}, err
		}
		return agent.CreateRunResponse{RunID: req.RunID, Created: won}, nil
	}
	for {
		id, err := newRunID()
		if err != nil {
			return agent.CreateRunResponse{}, err
		}
		won, err := s.claimMeta(ctx, id, meta)
		if err != nil {
			return agent.CreateRunResponse{}, err
		}
		if won {
			return agent.CreateRunResponse{RunID: id, Created: true}, nil
		}
	}
}

// exists reports whether the run's meta key is present.
func (s *RunStore) exists(ctx context.Context, id string) (bool, error) {
	n, err := s.client.Exists(ctx, s.key(id, "meta")).Result()
	return n > 0, err
}

// appendJSON pushes one JSON-encoded entry per value onto the run's
// list, gated on the run existing.
func appendJSON[T any](ctx context.Context, s *RunStore, id, part string, values []T) (bool, error) {
	ok, err := s.exists(ctx, id)
	if err != nil || !ok {
		return false, err
	}
	if len(values) == 0 {
		return true, nil
	}
	encoded := make([]any, len(values))
	for i, v := range values {
		body, err := json.Marshal(v)
		if err != nil {
			return false, fmt.Errorf("redisstore: encoding %s entry: %w", part, err)
		}
		encoded[i] = body
	}
	if err := s.client.RPush(ctx, s.key(id, part), encoded...).Err(); err != nil {
		return false, err
	}
	return true, nil
}

// AppendMessages implements agent.RunStore.
func (s *RunStore) AppendMessages(ctx context.Context, req agent.AppendMessagesRequest) (agent.AppendMessagesResponse, error) {
	found, err := appendJSON(ctx, s, req.RunID, "messages", req.Messages)
	return agent.AppendMessagesResponse{Found: found}, err
}

// AppendEvents implements agent.RunStore.
func (s *RunStore) AppendEvents(ctx context.Context, req agent.AppendEventsRequest) (agent.AppendEventsResponse, error) {
	found, err := appendJSON(ctx, s, req.RunID, "events", req.Events)
	return agent.AppendEventsResponse{Found: found}, err
}

// loadList reads and decodes a run's whole list. A corrupt entry is a
// storage-layer failure (someone wrote a non-JSON body), surfaced as an
// error rather than dropped.
func loadList[T any](ctx context.Context, s *RunStore, id, part string) ([]T, error) {
	raw, err := s.client.LRange(ctx, s.key(id, part), 0, -1).Result()
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]T, len(raw))
	for i, body := range raw {
		if err := json.Unmarshal([]byte(body), &out[i]); err != nil {
			return nil, fmt.Errorf("redisstore: corrupt %s entry %d in run %q: %w", part, i, id, err)
		}
	}
	return out, nil
}

// LoadRun implements agent.RunStore. The returned Run is freshly
// decoded, so it never aliases store state.
func (s *RunStore) LoadRun(ctx context.Context, req agent.LoadRunRequest) (agent.LoadRunResponse, error) {
	body, err := s.client.Get(ctx, s.key(req.RunID, "meta")).Result()
	if errors.Is(err, redis.Nil) {
		return agent.LoadRunResponse{}, nil
	}
	if err != nil {
		return agent.LoadRunResponse{}, err
	}
	var meta runMeta
	if err := json.Unmarshal([]byte(body), &meta); err != nil {
		return agent.LoadRunResponse{}, fmt.Errorf("redisstore: corrupt meta for run %q: %w", req.RunID, err)
	}
	messages, err := loadList[agent.Message](ctx, s, req.RunID, "messages")
	if err != nil {
		return agent.LoadRunResponse{}, err
	}
	events, err := loadList[agent.Event](ctx, s, req.RunID, "events")
	if err != nil {
		return agent.LoadRunResponse{}, err
	}
	return agent.LoadRunResponse{
		Run: agent.Run{
			ID:        req.RunID,
			ParentID:  meta.ParentID,
			CreatedAt: meta.CreatedAt,
			Messages:  messages,
			Events:    events,
		},
		Found: true,
	}, nil
}

// ForkRun implements agent.RunStore. See the RunStore doc for the
// snapshot-then-copy race window.
func (s *RunStore) ForkRun(ctx context.Context, req agent.ForkRunRequest) (agent.ForkRunResponse, error) {
	ok, err := s.exists(ctx, req.RunID)
	if err != nil {
		return agent.ForkRunResponse{}, err
	}
	if !ok {
		return agent.ForkRunResponse{}, nil
	}

	meta := runMeta{ParentID: req.RunID, CreatedAt: time.Now().UTC()}
	id := req.NewRunID
	if id != "" {
		won, err := s.claimMeta(ctx, id, meta)
		if err != nil {
			return agent.ForkRunResponse{}, err
		}
		if !won {
			return agent.ForkRunResponse{RunID: id, Found: true, Created: false}, nil
		}
	} else {
		for {
			id, err = newRunID()
			if err != nil {
				return agent.ForkRunResponse{}, err
			}
			won, err := s.claimMeta(ctx, id, meta)
			if err != nil {
				return agent.ForkRunResponse{}, err
			}
			if won {
				break
			}
		}
	}

	for _, part := range []string{"messages", "events"} {
		raw, err := s.client.LRange(ctx, s.key(req.RunID, part), 0, -1).Result()
		if err != nil {
			return agent.ForkRunResponse{}, err
		}
		if len(raw) == 0 {
			continue
		}
		entries := make([]any, len(raw))
		for i, b := range raw {
			entries[i] = b
		}
		if err := s.client.RPush(ctx, s.key(id, part), entries...).Err(); err != nil {
			return agent.ForkRunResponse{}, err
		}
	}
	return agent.ForkRunResponse{RunID: id, Found: true, Created: true}, nil
}
