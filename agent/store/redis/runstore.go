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
	"slices"
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
//   - ForkRun is atomic: the whole fork runs as one Lua script with the
//     meta write as its last effect, so it is an exact cut of the
//     source and a failed fork leaves no observable run (see ForkRun).
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
	ForkPoint int       `json:"forkPoint,omitempty"`
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

// AppendMessages implements agent.RunStore, stamping zero Timestamps
// per the agent.AppendMessagesRequest rule before encoding, so the
// stamp lives inside the stored JSON body and forks copy it for free.
func (s *RunStore) AppendMessages(ctx context.Context, req agent.AppendMessagesRequest) (agent.AppendMessagesResponse, error) {
	msgs := slices.Clone(req.Messages)
	now := time.Now().UTC()
	for i := range msgs {
		if msgs[i].Timestamp.IsZero() {
			msgs[i].Timestamp = now
		}
	}
	found, err := appendJSON(ctx, s, req.RunID, "messages", msgs)
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
			ForkPoint: meta.ForkPoint,
			CreatedAt: meta.CreatedAt,
			Messages:  messages,
			Events:    events,
		},
		Found: true,
	}, nil
}

// forkScript performs the whole fork — source check, target-claim
// check, stale-garbage cleanup, both log copies, and the meta write
// that commits the run into existence — as one atomic Lua execution.
// Redis runs a script single-threaded, so no partially-forked run is
// ever observable: the meta write is the commit point and it is the
// script's last effect. The DEL first means list entries left at an
// unclaimed target (a crashed pre-atomicity fork, or ad-hoc writes)
// can never merge into the fork.
//
// KEYS: 1 srcMeta, 2 srcMessages, 3 srcEvents,
//
//	4 dstMeta, 5 dstMessages, 6 dstEvents
//
// ARGV: 1 dst meta JSON, 2 message-copy bound (copy the first n
// messages; 0 copies none), 3 copy-events flag ("1" copies the whole
// event log, anything else skips it — partial forks carry no events,
// see agent.ForkRunRequest). Returns 0 = source missing, 1 = target
// already claimed, 2 = committed.
const forkScript = `
if redis.call('EXISTS', KEYS[1]) == 0 then return 0 end
if redis.call('EXISTS', KEYS[4]) == 1 then return 1 end
redis.call('DEL', KEYS[5], KEYS[6])
local n = tonumber(ARGV[2])
if n > 0 then
  local msgs = redis.call('LRANGE', KEYS[2], 0, n - 1)
  for i = 1, #msgs do redis.call('RPUSH', KEYS[5], msgs[i]) end
end
if ARGV[3] == '1' then
  local evts = redis.call('LRANGE', KEYS[3], 0, -1)
  for i = 1, #evts do redis.call('RPUSH', KEYS[6], evts[i]) end
end
redis.call('SET', KEYS[4], ARGV[1])
return 2
`

// Script outcomes, matching forkScript's return values.
const (
	forkSourceMissing = 0
	forkTargetClaimed = 1
	forkCommitted     = 2
)

// ForkRun implements agent.RunStore. The fork is all-or-nothing (one
// Lua script; see forkScript): on error no run exists at the new ID,
// so NewRunID doubles as an idempotency key — retry a failed fork with
// the same deterministic ID and it starts clean; retry one that
// actually committed and Created=false reports the existing, complete
// fork (confirm lineage via LoadRun ParentID).
//
// Cluster caveat: the script touches both runs' keys, which a Redis
// Cluster may place in different slots. A session store is assumed to
// run against a single node or replicated setup; cluster deployments
// need hash-tagged prefixes to co-locate keys.
func (s *RunStore) ForkRun(ctx context.Context, req agent.ForkRunRequest) (agent.ForkRunResponse, error) {
	// The cut point is resolved against the source length observed here;
	// the script copies exactly the first n messages, so appends racing
	// the fork land after the cut (agent.ForkRunRequest semantics).
	srcLen, err := s.client.LLen(ctx, s.key(req.RunID, "messages")).Result()
	if err != nil {
		return agent.ForkRunResponse{}, err
	}
	n := int(srcLen)
	if req.AtMessage > 0 && req.AtMessage < n {
		n = req.AtMessage
	}
	copyEvents := "0"
	if n == int(srcLen) {
		copyEvents = "1"
	}
	body, err := json.Marshal(runMeta{ParentID: req.RunID, ForkPoint: n, CreatedAt: time.Now().UTC()})
	if err != nil {
		return agent.ForkRunResponse{}, fmt.Errorf("redisstore: encoding run meta: %w", err)
	}

	fork := func(id string) (int64, error) {
		keys := []string{
			s.key(req.RunID, "meta"), s.key(req.RunID, "messages"), s.key(req.RunID, "events"),
			s.key(id, "meta"), s.key(id, "messages"), s.key(id, "events"),
		}
		return s.client.Eval(ctx, forkScript, keys, string(body), n, copyEvents).Int64()
	}

	id := req.NewRunID
	if id != "" {
		outcome, err := fork(id)
		if err != nil {
			return agent.ForkRunResponse{}, err
		}
		switch outcome {
		case forkSourceMissing:
			return agent.ForkRunResponse{}, nil
		case forkTargetClaimed:
			return agent.ForkRunResponse{RunID: id, Found: true, Created: false}, nil
		}
		return agent.ForkRunResponse{RunID: id, Found: true, Created: true, ForkPoint: n}, nil
	}
	for {
		if id, err = newRunID(); err != nil {
			return agent.ForkRunResponse{}, err
		}
		outcome, err := fork(id)
		if err != nil {
			return agent.ForkRunResponse{}, err
		}
		switch outcome {
		case forkSourceMissing:
			return agent.ForkRunResponse{}, nil
		case forkCommitted:
			return agent.ForkRunResponse{RunID: id, Found: true, Created: true, ForkPoint: n}, nil
		}
	}
}
