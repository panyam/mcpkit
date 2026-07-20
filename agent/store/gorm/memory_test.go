package gormstore

import (
	"context"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/panyam/mcpkit/agent"
)

// openPgvector connects to the test Postgres, ensures the pgvector extension
// exists, and returns a clean DB. It skips (not fails) when no Postgres env
// is configured or the extension can't be created — so `just test` (sqlite,
// no Docker) and a plain-Postgres CI both skip the semantic store, while
// `just testpg` against the pgvector image exercises it.
func openPgvector(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := postgresDSN()
	if dsn == "" {
		t.Skip("no MCPKIT_AGENT_TEST_PG* env; skipping pgvector semantic store")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("postgres open: %v", err)
	}
	if err := db.Exec("CREATE EXTENSION IF NOT EXISTS vector").Error; err != nil {
		t.Skipf("pgvector extension unavailable (%v); skipping semantic store", err)
	}
	if err := db.Exec("DROP TABLE IF EXISTS agent_memories").Error; err != nil {
		t.Fatalf("drop table: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("raw DB: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// newSemanticStore builds a store over the StubEmbedder (64-dim, deterministic,
// no network) so the semantic ranking is testable without a live model.
func newSemanticStore(t *testing.T, opts ...MemoryOption) *SemanticMemoryStore {
	t.Helper()
	db := openPgvector(t)
	opts = append([]MemoryOption{WithVectorDimensions(agent.DefaultStubEmbedderDim)}, opts...)
	s, err := NewSemanticMemoryStore(db, agent.StubEmbedder{}, opts...)
	if err != nil {
		t.Fatalf("NewSemanticMemoryStore: %v", err)
	}
	return s
}

func seedSemantic(t *testing.T, s *SemanticMemoryStore) {
	t.Helper()
	ctx := context.Background()
	for _, it := range []agent.MemoryItem{
		{Key: "lang", Value: "my favorite programming language is Go"},
		{Key: "editor", Value: "i edit code in vim every day"},
		{Key: "pet", Value: "my dog is a border collie named Pixel"},
	} {
		if _, err := s.PutMemory(ctx, agent.PutMemoryRequest{Item: it}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSemanticMemoryStore_RanksBySimilarity(t *testing.T) {
	s := newSemanticStore(t)
	seedSemantic(t, s)
	resp, err := s.ListMemories(context.Background(), agent.ListMemoriesRequest{Query: "which programming language do i like"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) != 3 {
		t.Fatalf("got %d items, want all 3 ranked", len(resp.Items))
	}
	if resp.Items[0].Item.Key != "lang" {
		t.Fatalf("top hit = %q (score %.3f), want lang", resp.Items[0].Item.Key, resp.Items[0].Score)
	}
	if resp.Items[0].Score <= resp.Items[2].Score {
		t.Fatalf("not ranked by descending score: %.3f then %.3f", resp.Items[0].Score, resp.Items[2].Score)
	}
}

func TestSemanticMemoryStore_Limit(t *testing.T) {
	s := newSemanticStore(t)
	seedSemantic(t, s)
	resp, err := s.ListMemories(context.Background(), agent.ListMemoriesRequest{Query: "programming language", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("Limit=2 returned %d items", len(resp.Items))
	}
}

func TestSemanticMemoryStore_EmptyQueryListsAllOldestFirst(t *testing.T) {
	s := newSemanticStore(t)
	seedSemantic(t, s)
	resp, err := s.ListMemories(context.Background(), agent.ListMemoriesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) != 3 {
		t.Fatalf("empty query = %d items, want all 3", len(resp.Items))
	}
	if resp.Items[0].Item.Key != "lang" || resp.Items[2].Item.Key != "pet" {
		t.Fatalf("empty-query order = %q..%q, want lang..pet (oldest first)", resp.Items[0].Item.Key, resp.Items[2].Item.Key)
	}
	if resp.Items[0].Score != 0 {
		t.Fatalf("empty query has no relevance score; want 0, got %v", resp.Items[0].Score)
	}
}

func TestSemanticMemoryStore_Delete(t *testing.T) {
	s := newSemanticStore(t)
	seedSemantic(t, s)
	ctx := context.Background()
	if r, err := s.DeleteMemory(ctx, agent.DeleteMemoryRequest{Key: "editor"}); err != nil || !r.Deleted {
		t.Fatalf("delete of stored key should report Deleted (err=%v)", err)
	}
	resp, _ := s.ListMemories(ctx, agent.ListMemoriesRequest{})
	if len(resp.Items) != 2 {
		t.Fatalf("after delete = %d items, want 2", len(resp.Items))
	}
	if r, err := s.DeleteMemory(ctx, agent.DeleteMemoryRequest{Key: "ghost"}); err != nil || r.Deleted {
		t.Fatalf("unknown key: want Deleted=false, err=nil; got Deleted=%v err=%v", r.Deleted, err)
	}
}

func TestSemanticMemoryStore_UpsertPreservesPosition(t *testing.T) {
	s := newSemanticStore(t)
	seedSemantic(t, s)
	ctx := context.Background()
	// Overwrite the oldest note; it must keep its listing position (created_at
	// preserved), matching the in-memory stores.
	if _, err := s.PutMemory(ctx, agent.PutMemoryRequest{Item: agent.MemoryItem{Key: "lang", Value: "actually i prefer Rust now"}}); err != nil {
		t.Fatal(err)
	}
	resp, _ := s.ListMemories(ctx, agent.ListMemoriesRequest{})
	if len(resp.Items) != 3 {
		t.Fatalf("upsert changed count: %d, want 3", len(resp.Items))
	}
	if resp.Items[0].Item.Key != "lang" {
		t.Fatalf("upsert reordered: first = %q, want lang", resp.Items[0].Item.Key)
	}
	if resp.Items[0].Item.Value != "actually i prefer Rust now" {
		t.Fatalf("upsert did not update value: %q", resp.Items[0].Item.Value)
	}
}

func TestSemanticMemoryStore_RejectsUnsafeTableName(t *testing.T) {
	// Construction validates the (raw-SQL-composed) table name before it
	// touches the DB, so a nil handle is fine — the identifier check fires
	// first. No Postgres needed, so this guards the injection surface in CI.
	for _, name := range []string{`agent_memories"; DROP TABLE x; --`, "has space", "a.b", "1bad"} {
		if _, err := NewSemanticMemoryStore(nil, agent.StubEmbedder{}, WithMemoryTableName(name)); err == nil {
			t.Errorf("table name %q: want rejection, got nil error", name)
		}
	}
}

func TestSemanticMemoryStore_NamespaceIsolation(t *testing.T) {
	db := openPgvector(t)
	ctx := context.Background()
	mk := func(ns string) *SemanticMemoryStore {
		s, err := NewSemanticMemoryStore(db, agent.StubEmbedder{},
			WithVectorDimensions(agent.DefaultStubEmbedderDim), WithMemoryNamespace(ns))
		if err != nil {
			t.Fatalf("NewSemanticMemoryStore(%q): %v", ns, err)
		}
		return s
	}
	a, b := mk("session-a"), mk("session-b")
	if _, err := a.PutMemory(ctx, agent.PutMemoryRequest{Item: agent.MemoryItem{Key: "k", Value: "secret for a"}}); err != nil {
		t.Fatal(err)
	}
	// b shares the table but a different namespace: it must not see a's note.
	resp, err := b.ListMemories(ctx, agent.ListMemoriesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) != 0 {
		t.Fatalf("namespace leak: session-b sees %d items, want 0", len(resp.Items))
	}
	resp, _ = a.ListMemories(ctx, agent.ListMemoriesRequest{})
	if len(resp.Items) != 1 {
		t.Fatalf("session-a lost its own note: %d items, want 1", len(resp.Items))
	}
}
