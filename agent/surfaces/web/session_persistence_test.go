package web

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/agent/host"
)

// persistedFactory returns a SessionManager factory that builds Apps with store
// attached (host.WithRunStore) — the invariant a store-backed manager relies on
// — over a fresh StubProvider scripted with one generic turn. Each Create/Get
// build is an independent App (its own provider), exactly as agentweb's factory
// produces per session.
func persistedFactory(t *testing.T, store agent.RunStore) func(context.Context) (*host.App, error) {
	t.Helper()
	return func(context.Context) (*host.App, error) {
		stub := agent.NewStubProvider(agent.StubTurn{Text: "ok", FinishReason: "stop"})
		cfg := &host.Config{Model: host.ModelConfig{BaseURL: "http://stub", Model: "stub-model"}}
		return host.NewApp(cfg, io.Discard, strings.NewReader(""),
			host.WithProvider(stub), host.WithRunStore(store))
	}
}

// TestSessionManager_SurvivesRestart is the persistence acceptance: a session
// created and driven through one manager rehydrates, history intact, from a
// FRESH manager over the same store — the process-restart shape. session_id is
// the run id, and Get on the fresh manager (whose live map never held the id)
// resumes the persisted run.
func TestSessionManager_SurvivesRestart(t *testing.T) {
	ctx := context.Background()
	store := agent.NewInMemoryRunStore()

	// Manager #1: create a session and run a turn.
	mgr1 := NewSessionManagerWithStore(persistedFactory(t, store), store)
	id, app1, err := mgr1.Create(ctx)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id == "" {
		t.Fatal("Create minted an empty session id")
	}
	if app1.RunID() != id {
		t.Fatalf("session id %q != run id %q (session_id must be the run id)", id, app1.RunID())
	}
	if err := app1.RunTurn(ctx, "remember 42"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	mgr1.CloseAll() // the process (and its live App) goes away

	// Manager #2 over the SAME store starts with an empty live map.
	mgr2 := NewSessionManagerWithStore(persistedFactory(t, store), store)
	if _, held := directGet(mgr2, id); held {
		t.Fatalf("fresh manager unexpectedly already holds %q live", id)
	}

	// Get rehydrates it from the store.
	app2, ok := mgr2.Get(ctx, id)
	if !ok {
		t.Fatalf("Get(%q) after restart: not found, want rehydrated", id)
	}

	// The rehydrated App carries the persisted conversation (resumed messages).
	res, err := app2.Dispatch(ctx, "/history")
	if err != nil {
		t.Fatalf("/history on rehydrated session: %v", err)
	}
	if len(res.Messages) != 2 {
		t.Fatalf("rehydrated history has %d messages, want 2: %+v", len(res.Messages), res.Messages)
	}
	if res.Messages[0].Text != "remember 42" {
		t.Fatalf("rehydrated first message = %q, want %q", res.Messages[0].Text, "remember 42")
	}

	// The persisted event stream survived too.
	run, err := store.LoadRun(ctx, agent.LoadRunRequest{RunID: id})
	if err != nil || !run.Found {
		t.Fatalf("LoadRun(%q) = (%+v, %v)", id, run, err)
	}
	if len(run.Run.Events) == 0 {
		t.Fatalf("persisted run %q carried no events", id)
	}
}

// TestSessionManager_ListReturnsPersistedRoster asserts List surfaces the
// durable roster: a run a fresh manager's live map never held still appears.
func TestSessionManager_ListReturnsPersistedRoster(t *testing.T) {
	ctx := context.Background()
	store := agent.NewInMemoryRunStore()

	mgr1 := NewSessionManagerWithStore(persistedFactory(t, store), store)
	id, _, err := mgr1.Create(ctx)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	mgr1.CloseAll()

	mgr2 := NewSessionManagerWithStore(persistedFactory(t, store), store)
	got := mgr2.List(ctx)
	if !contains(got, id) {
		t.Fatalf("List = %v, want to contain persisted %q the live map never held", got, id)
	}
}

// TestSessionManager_CloseKeepsRun asserts Close evicts the live App but leaves
// the run in the store, so a later Get rehydrates the session.
func TestSessionManager_CloseKeepsRun(t *testing.T) {
	ctx := context.Background()
	store := agent.NewInMemoryRunStore()

	mgr := NewSessionManagerWithStore(persistedFactory(t, store), store)
	id, app, err := mgr.Create(ctx)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := app.RunTurn(ctx, "hi"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if !mgr.Close(id) {
		t.Fatal("Close returned false for a live session")
	}

	// Evicted from the live cache, but the run persists — Get rehydrates it.
	app2, ok := mgr.Get(ctx, id)
	if !ok {
		t.Fatal("Get after Close: run should rehydrate, got not-found")
	}
	res, err := app2.Dispatch(ctx, "/history")
	if err != nil {
		t.Fatalf("/history after Close+rehydrate: %v", err)
	}
	if len(res.Messages) != 2 {
		t.Fatalf("rehydrated history has %d messages, want 2", len(res.Messages))
	}
}

// TestSessionManager_UnknownRunWithStoreNotFound asserts an unknown session id
// is not-found (not a crash) even when a store is configured — the store has no
// such run to resume, so Get reports app state, not an error.
func TestSessionManager_UnknownRunWithStoreNotFound(t *testing.T) {
	ctx := context.Background()
	store := agent.NewInMemoryRunStore()

	mgr := NewSessionManagerWithStore(persistedFactory(t, store), store)
	if _, ok := mgr.Get(ctx, "no-such-run"); ok {
		t.Fatal("Get(unknown) with a store should be not-found")
	}
}

// directGet reads the live cache WITHOUT triggering rehydration, so a test can
// assert whether an id is held live independent of the store-backed Get path.
func directGet(m *SessionManager, id string) (*host.App, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	app, ok := m.sessions[resolve(id)]
	return app, ok
}
