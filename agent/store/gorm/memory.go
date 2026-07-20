package gormstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/pgvector/pgvector-go"
	"gorm.io/gorm"

	"github.com/panyam/mcpkit/agent"
)

// DefaultMemoryTable is the table a SemanticMemoryStore uses when
// WithMemoryTableName is not set.
const DefaultMemoryTable = "agent_memories"

// DefaultEmbeddingDimensions is the vector width the store provisions its
// pgvector column with when WithVectorDimensions is not set. 1536 is the
// width of OpenAI text-embedding-3-small, the common default; a deployment
// using a different Embedder MUST pass WithVectorDimensions to match, since
// pgvector rejects an insert whose vector width differs from the column.
const DefaultEmbeddingDimensions = 1536

// SemanticMemoryStore is the durable, ANN-backed MemoryStore: Postgres with
// the pgvector extension. It is the sibling of agent.InMemorySemanticStore —
// same MemoryStore contract, same client-side embedding via the Embedder
// seam — but the vector index and top-k ranking live in the database instead
// of an in-process brute-force scan, so it survives process exit and scales
// past a working-memory scratchpad. Because it satisfies the same interface,
// recall never branches on the backend; swapping it in makes recall
// similarity-ranked and durable with no change to the model-facing tools.
//
// Postgres-only. Unlike RunStore and ToolResultStore in this module, there
// is no SQLite path — the vector column, the "<=>" cosine-distance operator,
// and the HNSW index are pgvector features. The "vector" extension is a
// prerequisite the store does not install itself (CREATE EXTENSION needs
// privileges the store should not assume); docker/backends' init SQL creates
// it, and NewSemanticMemoryStore fails clearly if the type is unknown.
//
// Scope: a namespace column lets one table hold many independent scratchpads
// (WithNamespace binds a store instance to one, empty = the global
// namespace). The per-request Namespace field is deferred to issue 1003; the
// column is already here so that additive interface change costs no schema
// migration — this store will read req.Namespace with the constructed value
// as the fallback.
//
// Concurrency: safe for concurrent use (the DB serializes). Embedding runs
// outside any transaction — a hosted Embedder is a network call, never held
// across a DB round-trip.
type SemanticMemoryStore struct {
	db        *gorm.DB
	embedder  agent.Embedder
	table     string
	namespace string
	dims      int
}

var _ agent.MemoryStore = (*SemanticMemoryStore)(nil)

type memoryConfig struct {
	skipAutoMigrate bool
	table           string
	namespace       string
	dims            int
}

// MemoryOption customizes a SemanticMemoryStore. Distinct from the RunStore
// and ToolResultStore option types so the stores' options never mix.
type MemoryOption func(*memoryConfig)

// WithMemoryTableName points the store at a specific table, so it can live
// inside an existing schema next to unrelated tables. Empty keeps
// DefaultMemoryTable.
func WithMemoryTableName(name string) MemoryOption {
	return func(c *memoryConfig) {
		if name != "" {
			c.table = name
		}
	}
}

// WithMemoryNamespace binds the store to one namespace (scratchpad). Every
// Put/List/Delete is scoped to it, so a host can build one store per session
// id and keep the scratchpads isolated in a shared table. Empty (the
// default) is the global namespace.
func WithMemoryNamespace(ns string) MemoryOption {
	return func(c *memoryConfig) { c.namespace = ns }
}

// WithVectorDimensions sets the width of the pgvector column, which MUST
// match the Embedder's output width — pgvector rejects a mismatched insert.
// Zero keeps DefaultEmbeddingDimensions.
func WithVectorDimensions(n int) MemoryOption {
	return func(c *memoryConfig) {
		if n > 0 {
			c.dims = n
		}
	}
}

// WithoutMemoryAutoMigrate disables the DDL run at construction, for
// deployments whose schema (table + HNSW index) is managed out of band.
func WithoutMemoryAutoMigrate() MemoryOption {
	return func(c *memoryConfig) { c.skipAutoMigrate = true }
}

// NewSemanticMemoryStore returns a store over db, embedding notes and queries
// with embedder. Unless WithoutMemoryAutoMigrate is passed it creates the
// table and an HNSW cosine index (idempotent), which requires the pgvector
// "vector" extension to already exist. The db handle and embedder are shared,
// not owned.
func NewSemanticMemoryStore(db *gorm.DB, embedder agent.Embedder, opts ...MemoryOption) (*SemanticMemoryStore, error) {
	if embedder == nil {
		return nil, fmt.Errorf("gormstore: SemanticMemoryStore needs an Embedder")
	}
	cfg := &memoryConfig{table: DefaultMemoryTable, dims: DefaultEmbeddingDimensions}
	for _, o := range opts {
		o(cfg)
	}
	s := &SemanticMemoryStore{
		db:        db,
		embedder:  embedder,
		table:     cfg.table,
		namespace: cfg.namespace,
		dims:      cfg.dims,
	}
	if !cfg.skipAutoMigrate {
		if err := s.migrate(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// migrate creates the memory table and the HNSW cosine index. The vector
// column width is fixed at construction (cfg.dims); the extension is assumed
// present. Table/index names derive from the configured table so multiple
// stores can coexist in one database.
func (s *SemanticMemoryStore) migrate() error {
	ddl := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %q (
		namespace  text        NOT NULL DEFAULT '',
		key        text        NOT NULL,
		body       jsonb       NOT NULL,
		embedding  vector(%d)  NOT NULL,
		created_at timestamptz NOT NULL,
		PRIMARY KEY (namespace, key)
	)`, s.table, s.dims)
	if err := s.db.Exec(ddl).Error; err != nil {
		return fmt.Errorf("gormstore: create memory table (is the pgvector extension installed?): %w", err)
	}
	idx := fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %q ON %q USING hnsw (embedding vector_cosine_ops)`,
		s.table+"_embedding_idx", s.table)
	if err := s.db.Exec(idx).Error; err != nil {
		return fmt.Errorf("gormstore: create memory index: %w", err)
	}
	return nil
}

// PutMemory embeds "key value" (so a recall query matches either) and upserts
// the note. On conflict the body and embedding are refreshed but created_at
// is preserved, so an update keeps the note's listing position — the same
// stable-ordering contract as the in-memory stores. Embedding is synchronous:
// a just-remembered fact is immediately recallable.
func (s *SemanticMemoryStore) PutMemory(ctx context.Context, req agent.PutMemoryRequest) (agent.PutMemoryResponse, error) {
	item := req.Item
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now().UTC()
	}
	vecs, err := s.embedder.Embed(ctx, []string{item.Key + " " + item.Value})
	if err != nil {
		return agent.PutMemoryResponse{}, fmt.Errorf("gormstore: embedding memory %q: %w", item.Key, err)
	}
	if len(vecs) == 0 {
		return agent.PutMemoryResponse{}, fmt.Errorf("gormstore: embedder returned no vector for memory %q", item.Key)
	}
	body, err := json.Marshal(item)
	if err != nil {
		return agent.PutMemoryResponse{}, fmt.Errorf("gormstore: encoding memory %q: %w", item.Key, err)
	}
	sql := fmt.Sprintf(`INSERT INTO %q (namespace, key, body, embedding, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (namespace, key) DO UPDATE SET body = EXCLUDED.body, embedding = EXCLUDED.embedding`, s.table)
	err = s.db.WithContext(ctx).Exec(sql,
		s.namespace, item.Key, string(body), pgvector.NewVector(vecs[0]), item.CreatedAt.UTC(),
	).Error
	if err != nil {
		return agent.PutMemoryResponse{}, err
	}
	return agent.PutMemoryResponse{}, nil
}

// ListMemories ranks notes in this namespace by cosine similarity to
// req.Query, most-relevant first, capped at req.Limit (0 = no cap). Score is
// 1 minus pgvector's cosine distance, so it matches agent.Embedding.Cosine
// (identical direction = 1). An empty Query is the "list everything" path the
// summary uses: it returns all notes oldest-first with Score 0, since there
// is no query to rank against.
func (s *SemanticMemoryStore) ListMemories(ctx context.Context, req agent.ListMemoriesRequest) (agent.ListMemoriesResponse, error) {
	if req.Query == "" {
		return s.listAll(ctx, req.Limit)
	}
	qvecs, err := s.embedder.Embed(ctx, []string{req.Query})
	if err != nil {
		return agent.ListMemoriesResponse{}, fmt.Errorf("gormstore: embedding query: %w", err)
	}
	if len(qvecs) == 0 {
		return agent.ListMemoriesResponse{}, fmt.Errorf("gormstore: embedder returned no vector for query")
	}
	sql := fmt.Sprintf(`SELECT body, 1 - (embedding <=> ?) AS score
		FROM %q WHERE namespace = ?
		ORDER BY embedding <=> ?`, s.table)
	args := []any{pgvector.NewVector(qvecs[0]), s.namespace, pgvector.NewVector(qvecs[0])}
	if req.Limit > 0 {
		sql += " LIMIT ?"
		args = append(args, req.Limit)
	}
	return s.scanScored(ctx, sql, args)
}

func (s *SemanticMemoryStore) listAll(ctx context.Context, limit int) (agent.ListMemoriesResponse, error) {
	sql := fmt.Sprintf(`SELECT body, 0 AS score FROM %q WHERE namespace = ?
		ORDER BY created_at, key`, s.table)
	args := []any{s.namespace}
	if limit > 0 {
		sql += " LIMIT ?"
		args = append(args, limit)
	}
	return s.scanScored(ctx, sql, args)
}

// scanRow is one row of the ranked query: the stored MemoryItem JSON plus its
// per-query similarity score.
type scanRow struct {
	Body  string
	Score float64
}

func (s *SemanticMemoryStore) scanScored(ctx context.Context, sql string, args []any) (agent.ListMemoriesResponse, error) {
	var rows []scanRow
	if err := s.db.WithContext(ctx).Raw(sql, args...).Scan(&rows).Error; err != nil {
		return agent.ListMemoriesResponse{}, err
	}
	out := make([]agent.ScoredMemory, 0, len(rows))
	for _, r := range rows {
		var item agent.MemoryItem
		if err := json.Unmarshal([]byte(r.Body), &item); err != nil {
			return agent.ListMemoriesResponse{}, fmt.Errorf("gormstore: corrupt memory row: %w", err)
		}
		out = append(out, agent.ScoredMemory{Item: item, Score: r.Score})
	}
	return agent.ListMemoriesResponse{Items: out}, nil
}

// DeleteMemory removes a note by key within this namespace. An unknown key is
// Deleted=false, not an error (the MemoryStore contract).
func (s *SemanticMemoryStore) DeleteMemory(ctx context.Context, req agent.DeleteMemoryRequest) (agent.DeleteMemoryResponse, error) {
	sql := fmt.Sprintf(`DELETE FROM %q WHERE namespace = ? AND key = ?`, s.table)
	res := s.db.WithContext(ctx).Exec(sql, s.namespace, req.Key)
	if res.Error != nil {
		return agent.DeleteMemoryResponse{}, res.Error
	}
	return agent.DeleteMemoryResponse{Deleted: res.RowsAffected > 0}, nil
}
