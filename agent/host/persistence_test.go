package host

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/agent"
)

func newPersistedApp(t *testing.T, store agent.RunStore, stub *agent.StubProvider, out *strings.Builder) *App {
	t.Helper()
	ts := startTestServer(t)
	app, err := NewApp(testConfig(ts.URL), out, strings.NewReader(""), WithProvider(stub), WithRunStore(store))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	return app
}

func TestAppPersistsTurns(t *testing.T) {
	ctx := context.Background()
	store := agent.NewInMemoryRunStore()
	stub := agent.NewStubProvider(
		agent.StubTurn{Text: "first answer"},
		agent.StubTurn{Text: "second answer"},
	)
	var out strings.Builder
	app := newPersistedApp(t, store, stub, &out)

	if got := app.RunID(); got != "" {
		t.Fatalf("RunID before first turn = %q, want empty (lazy create)", got)
	}
	if err := app.RunTurn(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	runID := app.RunID()
	if runID == "" {
		t.Fatal("RunID empty after a persisted turn")
	}
	if !strings.Contains(out.String(), "session: "+runID) {
		t.Fatalf("transcript missing session line:\n%s", out.String())
	}
	if err := app.RunTurn(ctx, "two"); err != nil {
		t.Fatal(err)
	}

	resp, err := store.LoadRun(ctx, agent.LoadRunRequest{RunID: runID})
	if err != nil || !resp.Found {
		t.Fatalf("LoadRun = (%+v, %v)", resp, err)
	}
	msgs := resp.Run.Messages
	if len(msgs) != 4 {
		t.Fatalf("persisted %d messages, want 4: %+v", len(msgs), msgs)
	}
	for i, want := range []string{"one", "first answer", "two", "second answer"} {
		if msgs[i].Text != want {
			t.Fatalf("persisted message %d = %q, want %q", i, msgs[i].Text, want)
		}
	}

	kinds := map[agent.EventKind]bool{}
	for _, e := range resp.Run.Events {
		kinds[e.Kind] = true
	}
	for _, want := range []agent.EventKind{agent.EventTurnBegin, agent.EventTextDelta, agent.EventTurnEnd} {
		if !kinds[want] {
			t.Fatalf("persisted event log missing %s: %+v", want, resp.Run.Events)
		}
	}
}

func TestAppResumeThreadsPersistedHistory(t *testing.T) {
	ctx := context.Background()
	store := agent.NewInMemoryRunStore()

	var out1 strings.Builder
	app1 := newPersistedApp(t, store, agent.NewStubProvider(agent.StubTurn{Text: "remembered"}), &out1)
	if err := app1.RunTurn(ctx, "fact"); err != nil {
		t.Fatal(err)
	}
	runID := app1.RunID()
	app1.Close()

	stub2 := agent.NewStubProvider(agent.StubTurn{Text: "recalled"})
	var out2 strings.Builder
	app2 := newPersistedApp(t, store, stub2, &out2)
	if err := app2.Resume(ctx, runID); err != nil {
		t.Fatal(err)
	}
	if err := app2.RunTurn(ctx, "what did I say?"); err != nil {
		t.Fatal(err)
	}

	msgs := stub2.Requests()[0].Messages
	if len(msgs) != 3 || msgs[0].Text != "fact" || msgs[1].Text != "remembered" || msgs[2].Text != "what did I say?" {
		t.Fatalf("resumed model request did not thread persisted history: %+v", msgs)
	}

	resp, _ := store.LoadRun(ctx, agent.LoadRunRequest{RunID: runID})
	if len(resp.Run.Messages) != 4 {
		t.Fatalf("resumed turn did not append to the run: %d messages, want 4", len(resp.Run.Messages))
	}
}

func TestAppResumeUnknownRunLeavesSessionIntact(t *testing.T) {
	ctx := context.Background()
	store := agent.NewInMemoryRunStore()
	stub := agent.NewStubProvider(agent.StubTurn{Text: "answer"})
	var out strings.Builder
	app := newPersistedApp(t, store, stub, &out)
	if err := app.RunTurn(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	runID := app.RunID()

	if err := app.Resume(ctx, "no-such-run"); err == nil {
		t.Fatal("Resume(unknown) succeeded, want error")
	}
	if got := app.RunID(); got != runID {
		t.Fatalf("failed Resume switched the run: %q, want %q", got, runID)
	}
	if len(app.history) != 2 {
		t.Fatalf("failed Resume mutated history: %+v", app.history)
	}
}

func TestAppForkDiverges(t *testing.T) {
	ctx := context.Background()
	store := agent.NewInMemoryRunStore()
	stub := agent.NewStubProvider(
		agent.StubTurn{Text: "shared"},
		agent.StubTurn{Text: "fork only"},
	)
	var out strings.Builder
	app := newPersistedApp(t, store, stub, &out)
	if err := app.RunTurn(ctx, "base"); err != nil {
		t.Fatal(err)
	}
	original := app.RunID()

	forkID, err := app.Fork(ctx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if forkID == original || app.RunID() != forkID {
		t.Fatalf("Fork = %q (active %q), original %q", forkID, app.RunID(), original)
	}
	if err := app.RunTurn(ctx, "diverge"); err != nil {
		t.Fatal(err)
	}

	orig, _ := store.LoadRun(ctx, agent.LoadRunRequest{RunID: original})
	fork, _ := store.LoadRun(ctx, agent.LoadRunRequest{RunID: forkID})
	if len(orig.Run.Messages) != 2 {
		t.Fatalf("original run polluted by fork turn: %+v", orig.Run.Messages)
	}
	if len(fork.Run.Messages) != 4 || fork.Run.Messages[3].Text != "fork only" {
		t.Fatalf("fork did not accumulate the divergent turn: %+v", fork.Run.Messages)
	}
	if fork.Run.ParentID != original {
		t.Fatalf("fork ParentID = %q, want %q", fork.Run.ParentID, original)
	}
}

func TestAppForkWithoutRunErrors(t *testing.T) {
	store := agent.NewInMemoryRunStore()
	stub := agent.NewStubProvider()
	var out strings.Builder
	app := newPersistedApp(t, store, stub, &out)
	if _, err := app.Fork(context.Background(), "", 0); err == nil {
		t.Fatal("Fork with no active run succeeded, want error")
	}
}

func TestAppAttachRunCreateOrResume(t *testing.T) {
	ctx := context.Background()
	store := agent.NewInMemoryRunStore()

	var out1 strings.Builder
	app1 := newPersistedApp(t, store, agent.NewStubProvider(agent.StubTurn{Text: "day one"}), &out1)
	if err := app1.AttachRun(ctx, "daily"); err != nil {
		t.Fatal(err)
	}
	if app1.RunID() != "daily" {
		t.Fatalf("AttachRun(new) RunID = %q", app1.RunID())
	}
	if err := app1.RunTurn(ctx, "note this"); err != nil {
		t.Fatal(err)
	}
	app1.Close()

	stub2 := agent.NewStubProvider(agent.StubTurn{Text: "day two"})
	var out2 strings.Builder
	app2 := newPersistedApp(t, store, stub2, &out2)
	if err := app2.AttachRun(ctx, "daily"); err != nil {
		t.Fatal(err)
	}
	if err := app2.RunTurn(ctx, "continue"); err != nil {
		t.Fatal(err)
	}
	msgs := stub2.Requests()[0].Messages
	if len(msgs) != 3 || msgs[0].Text != "note this" {
		t.Fatalf("AttachRun(existing) did not resume history: %+v", msgs)
	}
}

func TestAppFailedTurnPersistsNothing(t *testing.T) {
	ctx := context.Background()
	store := agent.NewInMemoryRunStore()
	stub := agent.NewStubProvider(
		agent.StubTurn{Text: "ok"},
		agent.StubTurn{Err: errors.New("model down")},
	)
	var out strings.Builder
	app := newPersistedApp(t, store, stub, &out)
	if err := app.RunTurn(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	if err := app.RunTurn(ctx, "two"); err == nil {
		t.Fatal("second turn succeeded, want provider error")
	}

	resp, _ := store.LoadRun(ctx, agent.LoadRunRequest{RunID: app.RunID()})
	if len(resp.Run.Messages) != 2 {
		t.Fatalf("failed turn leaked into the run: %+v", resp.Run.Messages)
	}
}

func TestAppWithoutStoreSessionMethodsError(t *testing.T) {
	ts := startTestServer(t)
	var out strings.Builder
	app, err := NewApp(testConfig(ts.URL), &out, strings.NewReader(""), WithProvider(agent.NewStubProvider()))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	ctx := context.Background()
	if err := app.Resume(ctx, "x"); err == nil {
		t.Fatal("Resume without store succeeded")
	}
	if err := app.AttachRun(ctx, "x"); err == nil {
		t.Fatal("AttachRun without store succeeded")
	}
	if _, err := app.Fork(ctx, "", 0); err == nil {
		t.Fatal("Fork without store succeeded")
	}
	if got := app.RunID(); got != "" {
		t.Fatalf("RunID without store = %q", got)
	}
}

func TestPersistingEmitFlush(t *testing.T) {
	ctx := context.Background()
	store := agent.NewInMemoryRunStore()
	created, err := store.CreateRun(ctx, agent.CreateRunRequest{})
	if err != nil {
		t.Fatal(err)
	}

	var forwarded []agent.Event
	pe := NewPersistingEmit(store, created.RunID, func(e agent.Event) { forwarded = append(forwarded, e) })
	pe.Emit(agent.Event{Kind: agent.EventTurnBegin})
	pe.Emit(agent.Event{Kind: agent.EventTextDelta, Step: 1, Text: "hi"})
	if len(forwarded) != 2 {
		t.Fatalf("wrapped handler saw %d events, want 2", len(forwarded))
	}

	if err := pe.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	resp, _ := store.LoadRun(ctx, agent.LoadRunRequest{RunID: created.RunID})
	if len(resp.Run.Events) != 2 {
		t.Fatalf("flushed %d events, want 2", len(resp.Run.Events))
	}
	if err := pe.Flush(ctx); err != nil {
		t.Fatalf("empty re-Flush: %v", err)
	}
	resp, _ = store.LoadRun(ctx, agent.LoadRunRequest{RunID: created.RunID})
	if len(resp.Run.Events) != 2 {
		t.Fatalf("re-Flush duplicated events: %d", len(resp.Run.Events))
	}

	missing := NewPersistingEmit(store, "gone", nil)
	missing.Emit(agent.Event{Kind: agent.EventTurnBegin})
	if err := missing.Flush(ctx); err == nil {
		t.Fatal("Flush against a missing run succeeded, want error")
	}
}

func TestAppForkAtRewindsHistory(t *testing.T) {
	ctx := context.Background()
	store := agent.NewInMemoryRunStore()
	stub := agent.NewStubProvider(
		agent.StubTurn{Text: "first answer"},
		agent.StubTurn{Text: "second answer"},
		agent.StubTurn{Text: "divergent answer"},
	)
	var out strings.Builder
	app := newPersistedApp(t, store, stub, &out)
	if err := app.RunTurn(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	if err := app.RunTurn(ctx, "two"); err != nil {
		t.Fatal(err)
	}
	original := app.RunID()

	forkID, err := app.Fork(ctx, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(app.history) != 2 {
		t.Fatalf("Fork at 2 left history at %d messages, want rewind to 2: %+v", len(app.history), app.history)
	}
	if err := app.RunTurn(ctx, "diverge"); err != nil {
		t.Fatal(err)
	}

	req := stub.Requests()[2].Messages
	if len(req) != 3 || req[0].Text != "one" || req[1].Text != "first answer" || req[2].Text != "diverge" {
		t.Fatalf("post-rewind model request = %+v, want first turn + diverge", req)
	}

	orig, _ := store.LoadRun(ctx, agent.LoadRunRequest{RunID: original})
	if len(orig.Run.Messages) != 4 {
		t.Fatalf("original run mutated by fork-at: %d messages, want 4", len(orig.Run.Messages))
	}
	fork, _ := store.LoadRun(ctx, agent.LoadRunRequest{RunID: forkID})
	if len(fork.Run.Messages) != 4 || fork.Run.Messages[2].Text != "diverge" {
		t.Fatalf("fork run = %+v, want 2 copied + 2 divergent", fork.Run.Messages)
	}
	if fork.Run.ForkPoint != 2 || fork.Run.ParentID != original {
		t.Fatalf("fork lineage = (parent %q, forkPoint %d), want (%q, 2)", fork.Run.ParentID, fork.Run.ForkPoint, original)
	}
}
