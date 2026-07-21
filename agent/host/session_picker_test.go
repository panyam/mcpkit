package host

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/agent"
)

func seedRuns(t *testing.T, store agent.RunStore, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := store.CreateRun(context.Background(), agent.CreateRunRequest{RunID: fmt.Sprintf("run-%03d", i)}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSessionsPage_ThreadsCursor(t *testing.T) {
	store := agent.NewInMemoryRunStore()
	seedRuns(t, store, 120)
	app := newPersistedApp(t, store, agent.NewStubProvider(), &strings.Builder{})
	ctx := context.Background()

	p1, err := app.SessionsPage(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(p1.Runs) != agent.DefaultListRunsLimit || !p1.HasMore {
		t.Fatalf("page1: %d runs hasMore=%v", len(p1.Runs), p1.HasMore)
	}
	p2, _ := app.PageMore(ctx)
	if len(p2.Runs) != agent.DefaultListRunsLimit || !p2.HasMore {
		t.Fatalf("page2: %d runs hasMore=%v", len(p2.Runs), p2.HasMore)
	}
	if p1.Runs[0].ID == p2.Runs[0].ID {
		t.Fatal("page2 repeats page1 (cursor not threaded)")
	}
	p3, _ := app.PageMore(ctx)
	if len(p3.Runs) != 20 || p3.HasMore {
		t.Fatalf("page3: %d runs hasMore=%v, want 20 and last", len(p3.Runs), p3.HasMore)
	}
	if p4, _ := app.PageMore(ctx); len(p4.Runs) != 0 {
		t.Fatalf("page after exhaustion: %d runs, want 0", len(p4.Runs))
	}
}

func TestSearchSessions_FiltersById(t *testing.T) {
	store := agent.NewInMemoryRunStore()
	seedRuns(t, store, 30)
	app := newPersistedApp(t, store, agent.NewStubProvider(), &strings.Builder{})
	got, err := app.SearchSessions(context.Background(), "RUN-01") // case-insensitive
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 10 { // run-010..run-019
		t.Fatalf("search run-01 = %d matches, want 10", len(got))
	}
}

func TestSessionsCommand_Routing(t *testing.T) {
	store := agent.NewInMemoryRunStore()
	seedRuns(t, store, 60)
	app := newPersistedApp(t, store, agent.NewStubProvider(), &strings.Builder{})
	ctx := context.Background()

	res, err := app.Dispatch(ctx, "/sessions")
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != CmdSessions || len(res.Sessions) != agent.DefaultListRunsLimit {
		t.Fatalf("/sessions: kind=%v n=%d", res.Kind, len(res.Sessions))
	}
	if !strings.Contains(res.SessionsNote, "more") {
		t.Fatalf("footer should offer 'more': %q", res.SessionsNote)
	}
	if res2, _ := app.Dispatch(ctx, "/sessions more"); res2.Kind != CmdSessions || len(res2.Sessions) != 10 {
		t.Fatalf("/sessions more: kind=%v n=%d, want 10", res2.Kind, len(res2.Sessions))
	}
	if res3, _ := app.Dispatch(ctx, "/sessions find run-02"); res3.Kind != CmdSessions || len(res3.Sessions) != 10 {
		t.Fatalf("/sessions find run-02: n=%d, want 10", len(res3.Sessions))
	}
	res4, err := app.Dispatch(ctx, "/sessions run-005")
	if err != nil {
		t.Fatal(err)
	}
	if res4.Kind != CmdSession || res4.RunID != "run-005" {
		t.Fatalf("/sessions run-005 (resume): kind=%v runID=%q", res4.Kind, res4.RunID)
	}
}
