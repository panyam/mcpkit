package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func mustCreate(t *testing.T, s RunStore, id string) string {
	t.Helper()
	resp, err := s.CreateRun(context.Background(), CreateRunRequest{RunID: id})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if !resp.Created {
		t.Fatalf("CreateRun(%q): Created=false", id)
	}
	return resp.RunID
}

func mustLoad(t *testing.T, s RunStore, id string) Run {
	t.Helper()
	resp, err := s.LoadRun(context.Background(), LoadRunRequest{RunID: id})
	if err != nil {
		t.Fatalf("LoadRun: %v", err)
	}
	if !resp.Found {
		t.Fatalf("LoadRun(%q): Found=false", id)
	}
	return resp.Run
}

func TestInMemoryRunStore_CreateAppendLoad(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryRunStore()

	id := mustCreate(t, s, "")
	if id == "" {
		t.Fatal("generated RunID is empty")
	}

	turn1 := []Message{
		{Role: RoleUser, Text: "hi"},
		{Role: RoleAssistant, Text: "hello"},
	}
	turn2 := []Message{
		{Role: RoleUser, Text: "list files"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "ls"}}},
		{Role: RoleTool, ToolCallID: "c1", Text: "a.go"},
		{Role: RoleAssistant, Text: "a.go"},
	}
	for _, msgs := range [][]Message{turn1, turn2} {
		resp, err := s.AppendMessages(ctx, AppendMessagesRequest{RunID: id, Messages: msgs})
		if err != nil {
			t.Fatalf("AppendMessages: %v", err)
		}
		if !resp.Found {
			t.Fatal("AppendMessages: Found=false for existing run")
		}
	}

	run := mustLoad(t, s, id)
	want := append(append([]Message{}, turn1...), turn2...)
	if !reflect.DeepEqual(stripStamps(t, run.Messages), want) {
		t.Fatalf("loaded messages mismatch:\n got %+v\nwant %+v", run.Messages, want)
	}
	if run.ID != id {
		t.Fatalf("Run.ID = %q, want %q", run.ID, id)
	}
	if run.ParentID != "" {
		t.Fatalf("Run.ParentID = %q, want empty for a direct run", run.ParentID)
	}
	if run.CreatedAt.IsZero() {
		t.Fatal("Run.CreatedAt is zero")
	}
}

func TestInMemoryRunStore_ExplicitAndDuplicateID(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryRunStore()

	if got := mustCreate(t, s, "session-a"); got != "session-a" {
		t.Fatalf("CreateRun kept explicit ID? got %q", got)
	}
	if _, err := s.AppendMessages(ctx, AppendMessagesRequest{
		RunID: "session-a", Messages: []Message{{Role: RoleUser, Text: "x"}},
	}); err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}

	dup, err := s.CreateRun(ctx, CreateRunRequest{RunID: "session-a"})
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

func TestInMemoryRunStore_UnknownRun(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryRunStore()

	if resp, err := s.LoadRun(ctx, LoadRunRequest{RunID: "nope"}); err != nil || resp.Found {
		t.Fatalf("LoadRun(unknown) = (%+v, %v), want Found=false, nil error", resp, err)
	}
	if resp, err := s.AppendMessages(ctx, AppendMessagesRequest{RunID: "nope"}); err != nil || resp.Found {
		t.Fatalf("AppendMessages(unknown) = (%+v, %v), want Found=false, nil error", resp, err)
	}
	if resp, err := s.AppendEvents(ctx, AppendEventsRequest{RunID: "nope"}); err != nil || resp.Found {
		t.Fatalf("AppendEvents(unknown) = (%+v, %v), want Found=false, nil error", resp, err)
	}
	if resp, err := s.ForkRun(ctx, ForkRunRequest{RunID: "nope"}); err != nil || resp.Found {
		t.Fatalf("ForkRun(unknown) = (%+v, %v), want Found=false, nil error", resp, err)
	}
}

func TestInMemoryRunStore_AppendEvents(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryRunStore()
	id := mustCreate(t, s, "")

	events := []Event{
		{Kind: EventTurnBegin},
		{Kind: EventTextDelta, Step: 1, Text: "hel"},
		{Kind: EventTextDelta, Step: 1, Text: "lo"},
	}
	resp, err := s.AppendEvents(ctx, AppendEventsRequest{RunID: id, Events: events})
	if err != nil || !resp.Found {
		t.Fatalf("AppendEvents = (%+v, %v)", resp, err)
	}
	run := mustLoad(t, s, id)
	if !reflect.DeepEqual(run.Events, events) {
		t.Fatalf("loaded events mismatch:\n got %+v\nwant %+v", run.Events, events)
	}
}

func TestInMemoryRunStore_ForkDiverges(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryRunStore()
	id := mustCreate(t, s, "parent")
	base := []Message{{Role: RoleUser, Text: "shared"}}
	if _, err := s.AppendMessages(ctx, AppendMessagesRequest{RunID: id, Messages: base}); err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}

	fork, err := s.ForkRun(ctx, ForkRunRequest{RunID: id})
	if err != nil {
		t.Fatalf("ForkRun: %v", err)
	}
	if !fork.Found || !fork.Created || fork.RunID == "" || fork.RunID == id {
		t.Fatalf("ForkRun = %+v", fork)
	}

	forked := mustLoad(t, s, fork.RunID)
	if !reflect.DeepEqual(stripStamps(t, forked.Messages), base) {
		t.Fatalf("fork did not copy the log: %+v", forked.Messages)
	}
	if forked.ParentID != id {
		t.Fatalf("fork ParentID = %q, want %q", forked.ParentID, id)
	}

	if _, err := s.AppendMessages(ctx, AppendMessagesRequest{
		RunID: fork.RunID, Messages: []Message{{Role: RoleUser, Text: "fork only"}},
	}); err != nil {
		t.Fatalf("AppendMessages(fork): %v", err)
	}
	if _, err := s.AppendMessages(ctx, AppendMessagesRequest{
		RunID: id, Messages: []Message{{Role: RoleUser, Text: "parent only"}},
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

func TestInMemoryRunStore_ForkExplicitIDCollision(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryRunStore()
	mustCreate(t, s, "src")
	mustCreate(t, s, "taken")

	fork, err := s.ForkRun(ctx, ForkRunRequest{RunID: "src", NewRunID: "taken"})
	if err != nil {
		t.Fatalf("ForkRun: %v", err)
	}
	if !fork.Found || fork.Created {
		t.Fatalf("ForkRun onto taken ID = %+v, want Found=true Created=false", fork)
	}
}

func TestInMemoryRunStore_LoadReturnsCopies(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryRunStore()
	id := mustCreate(t, s, "")
	if _, err := s.AppendMessages(ctx, AppendMessagesRequest{
		RunID: id, Messages: []Message{{Role: RoleUser, Text: "original"}},
	}); err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}

	run := mustLoad(t, s, id)
	run.Messages[0].Text = "mutated"
	run.Messages = append(run.Messages, Message{Role: RoleUser, Text: "extra"})

	again := mustLoad(t, s, id)
	if len(again.Messages) != 1 || again.Messages[0].Text != "original" {
		t.Fatalf("store state aliased by LoadRun result: %+v", again.Messages)
	}
}

// TestRun_JSONRoundTrip pins the property durable backends rely on: a Run
// (messages and events included) survives encoding/json unchanged, per
// constraint A2.
func TestRun_JSONRoundTrip(t *testing.T) {
	run := Run{
		ID:       "r1",
		ParentID: "r0",
		Messages: []Message{
			{Role: RoleUser, Text: "hi"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "ls"}}},
			{Role: RoleTool, ToolCallID: "c1", Text: "a.go"},
		},
		Events: []Event{
			{Kind: EventTurnBegin},
			{Kind: EventToolBegin, Step: 1, ToolCall: &ToolCall{ID: "c1", Name: "ls"}},
		},
	}
	data, err := json.Marshal(run)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Run
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	data2, err := json.Marshal(back)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if string(data) != string(data2) {
		t.Fatalf("round-trip drift:\n got %s\nwant %s", data2, data)
	}
}

func TestInMemoryRunStore_StampsTimestamps(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryRunStore()
	id := mustCreate(t, s, "")

	pre := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	if _, err := s.AppendMessages(ctx, AppendMessagesRequest{RunID: id, Messages: []Message{
		{Role: RoleUser, Text: "unstamped"},
		{Role: RoleAssistant, Text: "pre-stamped", Timestamp: pre},
	}}); err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}

	run := mustLoad(t, s, id)
	if run.Messages[0].Timestamp.IsZero() {
		t.Fatal("store did not stamp the unstamped message")
	}
	if !run.Messages[1].Timestamp.Equal(pre) {
		t.Fatalf("caller-set timestamp clobbered: %v, want %v", run.Messages[1].Timestamp, pre)
	}
}

// TestMessageTimestampOmittedWhenZero pins wire cleanliness: unstamped
// messages (the shape providers see mid-turn) carry no timestamp key.
func TestMessageTimestampOmittedWhenZero(t *testing.T) {
	raw, err := json.Marshal(Message{Role: RoleUser, Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "timestamp") {
		t.Fatalf("zero Timestamp leaked onto the wire: %s", raw)
	}
	stamped, err := json.Marshal(Message{Role: RoleUser, Text: "hi", Timestamp: time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stamped), `"timestamp":"2026-07-01T12:00:00Z"`) {
		t.Fatalf("stamped Timestamp missing from wire form: %s", stamped)
	}
}

// stripStamps asserts every message got stamped, then zeroes the field
// so structural comparisons stay exact on everything else.
func stripStamps(t *testing.T, msgs []Message) []Message {
	t.Helper()
	out := slices.Clone(msgs)
	for i := range out {
		if out[i].Timestamp.IsZero() {
			t.Fatalf("message %d not stamped: %+v", i, out[i])
		}
		out[i].Timestamp = time.Time{}
	}
	return out
}
