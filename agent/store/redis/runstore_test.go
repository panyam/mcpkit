package redisstore

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/core"
)

// newTestStore returns a RunStore over a fresh miniredis, or over a
// real Redis when MCPKIT_AGENT_TEST_REDIS_ADDR is set (flushed first so
// runs from other tests don't leak in).
func newTestStore(t *testing.T) *RunStore {
	t.Helper()
	addr := os.Getenv("MCPKIT_AGENT_TEST_REDIS_ADDR")
	if addr == "" {
		mr := miniredis.RunT(t)
		addr = mr.Addr()
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { client.Close() })
	if os.Getenv("MCPKIT_AGENT_TEST_REDIS_ADDR") != "" {
		if err := client.FlushDB(context.Background()).Err(); err != nil {
			t.Fatalf("flushing real redis: %v", err)
		}
	}
	return New(client)
}

func mustCreate(t *testing.T, s agent.RunStore, id string) string {
	t.Helper()
	resp, err := s.CreateRun(context.Background(), agent.CreateRunRequest{RunID: id})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if !resp.Created {
		t.Fatalf("CreateRun(%q): Created=false", id)
	}
	return resp.RunID
}

func mustLoad(t *testing.T, s agent.RunStore, id string) agent.Run {
	t.Helper()
	resp, err := s.LoadRun(context.Background(), agent.LoadRunRequest{RunID: id})
	if err != nil {
		t.Fatalf("LoadRun: %v", err)
	}
	if !resp.Found {
		t.Fatalf("LoadRun(%q): Found=false", id)
	}
	return resp.Run
}

func TestRedisRunStore_CreateAppendLoad(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	id := mustCreate(t, s, "")
	if id == "" {
		t.Fatal("generated RunID is empty")
	}

	turn1 := []agent.Message{
		{Role: agent.RoleUser, Text: "hi"},
		{Role: agent.RoleAssistant, Text: "hello"},
	}
	turn2 := []agent.Message{
		{Role: agent.RoleUser, Text: "list files"},
		{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{{
			ID: "c1", Name: "ls", Args: core.NewRawJSON(json.RawMessage(`{"path":"."}`)),
		}}},
		{Role: agent.RoleTool, ToolCallID: "c1", Text: "a.go"},
		{Role: agent.RoleAssistant, Text: "a.go"},
	}
	for _, msgs := range [][]agent.Message{turn1, turn2} {
		resp, err := s.AppendMessages(ctx, agent.AppendMessagesRequest{RunID: id, Messages: msgs})
		if err != nil {
			t.Fatalf("AppendMessages: %v", err)
		}
		if !resp.Found {
			t.Fatal("AppendMessages: Found=false for existing run")
		}
	}

	run := mustLoad(t, s, id)
	want := append(append([]agent.Message{}, turn1...), turn2...)
	assertMessagesEqual(t, run.Messages, want)
	if run.ID != id || run.ParentID != "" {
		t.Fatalf("Run identity = (%q, parent %q), want (%q, \"\")", run.ID, run.ParentID, id)
	}
	if run.CreatedAt.IsZero() {
		t.Fatal("Run.CreatedAt is zero")
	}
}

// assertMessagesEqual compares through the JSON wire form: RawJSON
// carries unexported lazy-parse state that reflect.DeepEqual would trip
// on, and the wire form is the contract anyway.
func assertMessagesEqual(t *testing.T, got, want []agent.Message) {
	t.Helper()
	g, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal got: %v", err)
	}
	w, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal want: %v", err)
	}
	if string(g) != string(w) {
		t.Fatalf("messages mismatch:\n got %s\nwant %s", g, w)
	}
}

func TestRedisRunStore_ExplicitAndDuplicateID(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if got := mustCreate(t, s, "session-a"); got != "session-a" {
		t.Fatalf("CreateRun kept explicit ID? got %q", got)
	}
	if _, err := s.AppendMessages(ctx, agent.AppendMessagesRequest{
		RunID: "session-a", Messages: []agent.Message{{Role: agent.RoleUser, Text: "x"}},
	}); err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}

	dup, err := s.CreateRun(ctx, agent.CreateRunRequest{RunID: "session-a"})
	if err != nil {
		t.Fatalf("duplicate CreateRun: %v", err)
	}
	if dup.Created {
		t.Fatal("duplicate CreateRun reported Created=true")
	}
	if got := mustLoad(t, s, "session-a"); len(got.Messages) != 1 {
		t.Fatalf("duplicate CreateRun clobbered the run: %d messages, want 1", len(got.Messages))
	}
}

func TestRedisRunStore_UnknownRun(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if resp, err := s.LoadRun(ctx, agent.LoadRunRequest{RunID: "nope"}); err != nil || resp.Found {
		t.Fatalf("LoadRun(unknown) = (%+v, %v), want Found=false, nil error", resp, err)
	}
	if resp, err := s.AppendMessages(ctx, agent.AppendMessagesRequest{RunID: "nope"}); err != nil || resp.Found {
		t.Fatalf("AppendMessages(unknown) = (%+v, %v), want Found=false, nil error", resp, err)
	}
	if resp, err := s.AppendEvents(ctx, agent.AppendEventsRequest{RunID: "nope"}); err != nil || resp.Found {
		t.Fatalf("AppendEvents(unknown) = (%+v, %v), want Found=false, nil error", resp, err)
	}
	if resp, err := s.ForkRun(ctx, agent.ForkRunRequest{RunID: "nope"}); err != nil || resp.Found {
		t.Fatalf("ForkRun(unknown) = (%+v, %v), want Found=false, nil error", resp, err)
	}
}

func TestRedisRunStore_AppendEvents(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	id := mustCreate(t, s, "")

	events := []agent.Event{
		{Kind: agent.EventTurnBegin},
		{Kind: agent.EventToolBegin, Step: 1, ToolCall: &agent.ToolCall{ID: "c1", Name: "ls"}},
		{Kind: agent.EventTextDelta, Step: 1, Text: "hello"},
	}
	resp, err := s.AppendEvents(ctx, agent.AppendEventsRequest{RunID: id, Events: events})
	if err != nil || !resp.Found {
		t.Fatalf("AppendEvents = (%+v, %v)", resp, err)
	}

	run := mustLoad(t, s, id)
	g, _ := json.Marshal(run.Events)
	w, _ := json.Marshal(events)
	if string(g) != string(w) {
		t.Fatalf("events mismatch:\n got %s\nwant %s", g, w)
	}
}

func TestRedisRunStore_ForkDiverges(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	id := mustCreate(t, s, "parent")
	base := []agent.Message{{Role: agent.RoleUser, Text: "shared"}}
	if _, err := s.AppendMessages(ctx, agent.AppendMessagesRequest{RunID: id, Messages: base}); err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}

	fork, err := s.ForkRun(ctx, agent.ForkRunRequest{RunID: id})
	if err != nil {
		t.Fatalf("ForkRun: %v", err)
	}
	if !fork.Found || !fork.Created || fork.RunID == "" || fork.RunID == id {
		t.Fatalf("ForkRun = %+v", fork)
	}

	forked := mustLoad(t, s, fork.RunID)
	assertMessagesEqual(t, forked.Messages, base)
	if forked.ParentID != id {
		t.Fatalf("fork ParentID = %q, want %q", forked.ParentID, id)
	}

	if _, err := s.AppendMessages(ctx, agent.AppendMessagesRequest{
		RunID: fork.RunID, Messages: []agent.Message{{Role: agent.RoleUser, Text: "fork only"}},
	}); err != nil {
		t.Fatalf("AppendMessages(fork): %v", err)
	}
	if _, err := s.AppendMessages(ctx, agent.AppendMessagesRequest{
		RunID: id, Messages: []agent.Message{{Role: agent.RoleUser, Text: "parent only"}},
	}); err != nil {
		t.Fatalf("AppendMessages(parent): %v", err)
	}

	parent := mustLoad(t, s, id)
	forked = mustLoad(t, s, fork.RunID)
	if len(parent.Messages) != 2 || parent.Messages[1].Text != "parent only" {
		t.Fatalf("parent log polluted: %+v", parent.Messages)
	}
	if len(forked.Messages) != 2 || forked.Messages[1].Text != "fork only" {
		t.Fatalf("fork log polluted: %+v", forked.Messages)
	}
}

func TestRedisRunStore_ForkExplicitIDCollision(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	mustCreate(t, s, "src")
	mustCreate(t, s, "taken")

	fork, err := s.ForkRun(ctx, agent.ForkRunRequest{RunID: "src", NewRunID: "taken"})
	if err != nil {
		t.Fatalf("ForkRun: %v", err)
	}
	if !fork.Found || fork.Created {
		t.Fatalf("ForkRun onto taken ID = %+v, want Found=true Created=false", fork)
	}
}

// TestRedisRunStore_SurvivesReconnect pins durability: a second store
// over a fresh client sees runs the first store wrote.
func TestRedisRunStore_SurvivesReconnect(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)

	c1 := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	s1 := New(c1)
	resp, err := s1.CreateRun(ctx, agent.CreateRunRequest{RunID: "durable"})
	if err != nil || !resp.Created {
		t.Fatalf("CreateRun = (%+v, %v)", resp, err)
	}
	if _, err := s1.AppendMessages(ctx, agent.AppendMessagesRequest{
		RunID: "durable", Messages: []agent.Message{{Role: agent.RoleUser, Text: "persisted"}},
	}); err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}
	c1.Close()

	c2 := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer c2.Close()
	run := mustLoad(t, New(c2), "durable")
	if len(run.Messages) != 1 || run.Messages[0].Text != "persisted" {
		t.Fatalf("run did not survive reconnect: %+v", run.Messages)
	}
}

func TestRedisRunStore_KeyPrefixIsolation(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	a := New(client, WithKeyPrefix("tenant-a"))
	b := New(client, WithKeyPrefix("tenant-b"))
	mustCreate(t, a, "shared-name")

	if resp, err := b.LoadRun(ctx, agent.LoadRunRequest{RunID: "shared-name"}); err != nil || resp.Found {
		t.Fatalf("prefix isolation broken: LoadRun = (%+v, %v)", resp, err)
	}
	if resp, err := b.CreateRun(ctx, agent.CreateRunRequest{RunID: "shared-name"}); err != nil || !resp.Created {
		t.Fatalf("prefix isolation broken: CreateRun = (%+v, %v)", resp, err)
	}
}

func TestRedisRunStore_ForkRetryConverges(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	mustCreate(t, s, "src")
	base := []agent.Message{{Role: agent.RoleUser, Text: "shared"}}
	if _, err := s.AppendMessages(ctx, agent.AppendMessagesRequest{RunID: "src", Messages: base}); err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}

	first, err := s.ForkRun(ctx, agent.ForkRunRequest{RunID: "src", NewRunID: "retry-fork"})
	if err != nil || !first.Found || !first.Created {
		t.Fatalf("first ForkRun = (%+v, %v)", first, err)
	}
	retry, err := s.ForkRun(ctx, agent.ForkRunRequest{RunID: "src", NewRunID: "retry-fork"})
	if err != nil {
		t.Fatalf("retry ForkRun: %v", err)
	}
	if !retry.Found || retry.Created {
		t.Fatalf("retry ForkRun = %+v, want Found=true Created=false", retry)
	}

	run := mustLoad(t, s, "retry-fork")
	assertMessagesEqual(t, run.Messages, base)
	if run.ParentID != "src" {
		t.Fatalf("fork ParentID = %q, want src (retry convergence check)", run.ParentID)
	}
}

// TestRedisRunStore_ForkDoesNotInheritPlantedGarbage pins the
// all-or-nothing contract: leftover list entries at an unclaimed
// NewRunID (what a crashed earlier fork attempt could leave behind)
// must not leak into a subsequent fork of the same name.
func TestRedisRunStore_ForkDoesNotInheritPlantedGarbage(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	mustCreate(t, s, "src")
	base := []agent.Message{{Role: agent.RoleUser, Text: "clean"}}
	if _, err := s.AppendMessages(ctx, agent.AppendMessagesRequest{RunID: "src", Messages: base}); err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}

	if err := s.client.RPush(ctx, s.key("crashed-fork", "messages"), `{"role":"user","text":"orphan"}`).Err(); err != nil {
		t.Fatalf("planting garbage: %v", err)
	}

	fork, err := s.ForkRun(ctx, agent.ForkRunRequest{RunID: "src", NewRunID: "crashed-fork"})
	if err != nil || !fork.Found || !fork.Created {
		t.Fatalf("ForkRun = (%+v, %v)", fork, err)
	}
	run := mustLoad(t, s, "crashed-fork")
	assertMessagesEqual(t, run.Messages, base)
}

func TestRedisRunStore_ListKeysWithoutMetaAreInvisible(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.client.RPush(ctx, s.key("ghost", "messages"), `{"role":"user","text":"x"}`).Err(); err != nil {
		t.Fatalf("planting ghost list: %v", err)
	}

	if resp, err := s.LoadRun(ctx, agent.LoadRunRequest{RunID: "ghost"}); err != nil || resp.Found {
		t.Fatalf("LoadRun(ghost) = (%+v, %v), want invisible", resp, err)
	}
	if resp, err := s.AppendMessages(ctx, agent.AppendMessagesRequest{
		RunID: "ghost", Messages: []agent.Message{{Role: agent.RoleUser, Text: "y"}},
	}); err != nil || resp.Found {
		t.Fatalf("AppendMessages(ghost) = (%+v, %v), want invisible", resp, err)
	}
	if resp, err := s.ForkRun(ctx, agent.ForkRunRequest{RunID: "ghost"}); err != nil || resp.Found {
		t.Fatalf("ForkRun(ghost) = (%+v, %v), want invisible", resp, err)
	}
}
