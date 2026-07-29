package agent

import (
	"context"
	"strings"
	"testing"
)

func poolRunner(t *testing.T, turns ...StubTurn) *Runner {
	t.Helper()
	r, err := NewRunner(RunnerConfig{Provider: NewStubProvider(turns...)})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAgentPool_SpawnAwait(t *testing.T) {
	pool := NewAgentPool(nil)
	if err := pool.Register("worker", "does work", poolRunner(t, StubTurn{Text: "the answer"}), 0); err != nil {
		t.Fatal(err)
	}

	id, err := pool.Spawn(context.Background(), "worker", "go")
	if err != nil {
		t.Fatal(err)
	}
	res, err := pool.Await(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "the answer" {
		t.Fatalf("await result = %q, want the child's answer", res.Text)
	}
	// Await is idempotent.
	if res2, err := pool.Await(context.Background(), id); err != nil || res2.Text != "the answer" {
		t.Fatalf("second await = (%+v, %v), want the same stored result", res2, err)
	}
}

func TestAgentPool_UnknownName(t *testing.T) {
	pool := NewAgentPool(nil)
	if _, err := pool.Spawn(context.Background(), "nope", "go"); err == nil {
		t.Fatal("spawning an unregistered agent should error")
	}
}

func TestAgentPool_UnknownHandle(t *testing.T) {
	pool := NewAgentPool(nil)
	if _, err := pool.Await(context.Background(), "agent-99"); err == nil {
		t.Fatal("await of an unknown handle should error")
	}
	if err := pool.Cancel("agent-99"); err == nil {
		t.Fatal("cancel of an unknown handle should error")
	}
}

func TestAgentPool_Cancel(t *testing.T) {
	// blockingProvider's stream blocks until ctx is cancelled, so the spawned
	// child cannot finish before Cancel — the status transition is deterministic.
	r, _ := NewRunner(RunnerConfig{Provider: blockingProvider{}})
	pool := NewAgentPool(nil)
	if err := pool.Register("slow", "blocks", r, 0); err != nil {
		t.Fatal(err)
	}

	id, err := pool.Spawn(context.Background(), "slow", "go")
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Cancel(id); err != nil {
		t.Fatal(err)
	}
	// Awaiting a cancelled agent errors (its run returned ctx.Canceled).
	if _, err := pool.Await(context.Background(), id); err == nil {
		t.Fatal("await of a cancelled agent should error")
	}
	for _, s := range pool.Statuses() {
		if s.ID == id && s.Status != "cancelled" {
			t.Fatalf("status = %q, want cancelled", s.Status)
		}
	}
}

func TestAgentPool_Statuses(t *testing.T) {
	pool := NewAgentPool(nil)
	_ = pool.Register("a", "a", poolRunner(t, StubTurn{Text: "x"}), 0)
	id, _ := pool.Spawn(context.Background(), "a", "go")
	if _, err := pool.Await(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	st := pool.Statuses()
	if len(st) != 1 || st[0].ID != id || st[0].Status != "done" {
		t.Fatalf("statuses = %+v, want one done handle", st)
	}
}

// TestSpawnSource_Tools drives the meta-tools end to end: spawn_agent returns a
// handle, await_agent returns the child's text.
func TestSpawnSource_Tools(t *testing.T) {
	pool := NewAgentPool(nil)
	_ = pool.Register("worker", "does work", poolRunner(t, StubTurn{Text: "tool answer"}), 0)
	src := NewSpawnSource(pool)

	spawned, err := src.Call(context.Background(), SpawnAgentToolName, map[string]any{"name": "worker", "task": "go"})
	if err != nil || spawned.IsError {
		t.Fatalf("spawn_agent = (%+v, %v)", spawned, err)
	}
	// the handle id is the last token of "spawned \"worker\" as agent-1 (...)"
	id := "agent-1"
	if !strings.Contains(spawned.Content[0].Text, id) {
		t.Fatalf("spawn result %q did not name the handle", spawned.Content[0].Text)
	}
	awaited, err := src.Call(context.Background(), AwaitAgentToolName, map[string]any{"id": id})
	if err != nil || awaited.IsError {
		t.Fatalf("await_agent = (%+v, %v)", awaited, err)
	}
	if awaited.Content[0].Text != "tool answer" {
		t.Fatalf("await_agent result = %q, want the child's text", awaited.Content[0].Text)
	}
}
