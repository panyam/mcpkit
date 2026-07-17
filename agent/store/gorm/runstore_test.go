package gormstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/core"
)

// forEachBackend runs the assertion body against SQLite (always; no
// Docker required) and Postgres (only when the MCPKIT_AGENT_TEST_PG*
// env vars are set — `make testpg` in this directory boots a container
// and exports them).
func forEachBackend(t *testing.T, run func(t *testing.T, s *RunStore)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		run(t, newStoreFromDB(t, openSQLite(t)))
	})
	if dsn := postgresDSN(); dsn != "" {
		t.Run("postgres", func(t *testing.T) {
			run(t, newStoreFromDB(t, openPostgres(t, dsn)))
		})
	}
}

func newStoreFromDB(t *testing.T, db *gorm.DB) *RunStore {
	t.Helper()
	s, err := New(db)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// openSQLite opens a private in-memory SQLite per call, capped to one
// pooled connection so concurrent writers serialize instead of hitting
// "database is locked" (the same rationale as the events gorm store).
func openSQLite(t *testing.T) *gorm.DB {
	t.Helper()
	return openSQLitePath(t, "file::memory:?_busy_timeout=5000")
}

func openSQLitePath(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("sqlite open: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sqlite raw DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// postgresDSN assembles the connection string from MCPKIT_AGENT_TEST_PG*
// env vars, or "" when any required var is missing.
func postgresDSN() string {
	host := getenv("MCPKIT_AGENT_TEST_PGHOST", "localhost")
	port := getenv("MCPKIT_AGENT_TEST_PGPORT", "5435")
	user := os.Getenv("MCPKIT_AGENT_TEST_PGUSER")
	pass := os.Getenv("MCPKIT_AGENT_TEST_PGPASSWORD")
	dbname := os.Getenv("MCPKIT_AGENT_TEST_PGDB")
	if user == "" || pass == "" || dbname == "" {
		return ""
	}
	return fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable", host, port, user, pass, dbname)
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// openPostgres connects and truncates before the test, since Postgres
// subtests share one database (unlike SQLite's per-test :memory:).
func openPostgres(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("postgres open: %v", err)
	}
	if err := db.AutoMigrate(&runRow{}, &runMessageRow{}, &runEventRow{}); err != nil {
		t.Fatalf("postgres automigrate: %v", err)
	}
	if err := db.Exec("TRUNCATE TABLE agent_runs, agent_run_messages, agent_run_events").Error; err != nil {
		t.Fatalf("postgres truncate: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("postgres raw DB: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
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

// assertMessagesEqual compares through the JSON wire form (RawJSON
// carries unexported lazy-parse state; the wire form is the contract).
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

func TestGormRunStore_CreateAppendLoad(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *RunStore) {
		ctx := context.Background()
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
	})
}

func TestGormRunStore_ExplicitAndDuplicateID(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *RunStore) {
		ctx := context.Background()
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
	})
}

func TestGormRunStore_UnknownRun(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *RunStore) {
		ctx := context.Background()
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
	})
}

func TestGormRunStore_AppendEvents(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *RunStore) {
		ctx := context.Background()
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
	})
}

func TestGormRunStore_ForkDiverges(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *RunStore) {
		ctx := context.Background()
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
	})
}

func TestGormRunStore_ForkExplicitIDCollision(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *RunStore) {
		ctx := context.Background()
		mustCreate(t, s, "src")
		mustCreate(t, s, "taken")
		fork, err := s.ForkRun(ctx, agent.ForkRunRequest{RunID: "src", NewRunID: "taken"})
		if err != nil {
			t.Fatalf("ForkRun: %v", err)
		}
		if !fork.Found || fork.Created {
			t.Fatalf("ForkRun onto taken ID = %+v, want Found=true Created=false", fork)
		}
	})
}

// TestGormRunStore_ForkPreservesLongOrder pins that the INSERT ... SELECT
// copy keeps seq order for a log long enough to tempt a planner into
// reordering.
func TestGormRunStore_ForkPreservesLongOrder(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *RunStore) {
		ctx := context.Background()
		id := mustCreate(t, s, "")
		var want []agent.Message
		for i := 0; i < 200; i++ {
			want = append(want, agent.Message{Role: agent.RoleUser, Text: fmt.Sprintf("m%03d", i)})
		}
		if _, err := s.AppendMessages(ctx, agent.AppendMessagesRequest{RunID: id, Messages: want}); err != nil {
			t.Fatalf("AppendMessages: %v", err)
		}
		fork, err := s.ForkRun(ctx, agent.ForkRunRequest{RunID: id})
		if err != nil {
			t.Fatalf("ForkRun: %v", err)
		}
		assertMessagesEqual(t, mustLoad(t, s, fork.RunID).Messages, want)
	})
}

// TestGormRunStore_SurvivesReopen pins durability: a second store over a
// fresh connection to the same database file sees runs the first wrote.
func TestGormRunStore_SurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runs.db")

	s1 := newStoreFromDB(t, openSQLitePath(t, path+"?_busy_timeout=5000"))
	resp, err := s1.CreateRun(ctx, agent.CreateRunRequest{RunID: "durable"})
	if err != nil || !resp.Created {
		t.Fatalf("CreateRun = (%+v, %v)", resp, err)
	}
	if _, err := s1.AppendMessages(ctx, agent.AppendMessagesRequest{
		RunID: "durable", Messages: []agent.Message{{Role: agent.RoleUser, Text: "persisted"}},
	}); err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}
	db1, _ := s1.db.DB()
	db1.Close()

	s2 := newStoreFromDB(t, openSQLitePath(t, path+"?_busy_timeout=5000"))
	run := mustLoad(t, s2, "durable")
	if len(run.Messages) != 1 || run.Messages[0].Text != "persisted" {
		t.Fatalf("run did not survive reopen: %+v", run.Messages)
	}
}
