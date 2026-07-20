// Package gormstore is the relational backend for agent.RunStore,
// targeting Postgres via GORM (SQLite works and is the test default).
//
// It is a sibling module (its own go.mod) so GORM and the database
// drivers stay out of agent/, mirroring agent/store/redis and the
// events SDK's stores/gorm split. The wire format is encoding/json —
// agent messages and events are wire-serializable by constraint A2.
//
// Schema, managed by AutoMigrate on construction (disable with
// WithoutAutoMigrate where an out-of-band migration tool owns the
// schema):
//
//	agent_runs          id (PK), parent_id, created_at
//	agent_run_messages  seq (PK, autoincrement), run_id (indexed), body
//	agent_run_events    seq (PK, autoincrement), run_id (indexed), body
//
// Bodies are JSON documents in jsonb columns (Postgres; SQLite stores
// them as text), one agent.Message / agent.Event per row, load-ordered
// by seq. The run row is the existence marker: CreateRun claims it with
// INSERT ... ON CONFLICT DO NOTHING, and ForkRun copies both log tables
// inside one transaction — unlike the Redis backend, a fork here is an
// exact cut: appends racing the fork either commit before it (included)
// or after it (excluded), never half-copied.
//
// Schema changes follow the repo's fresh-deploys-only convention: no
// migration recipes ship with column changes; recreate the tables (see
// DEPLOYMENT.md in the events SDK for the rationale).
//
// The module also ships two agent-memory backends behind the same package:
//
//	ToolResultStore        agent.ToolResultStore (Postgres or SQLite)
//	SemanticMemoryStore    agent.MemoryStore, Postgres + pgvector ONLY
//
// SemanticMemoryStore is the durable counterpart to
// agent.InMemorySemanticStore: it embeds notes and queries client-side
// (the agent.Embedder seam) and does ANN top-k in SQL over a pgvector
// column ("ORDER BY embedding <=> $query LIMIT k"), so recall survives
// process exit and scales past a scratchpad. It has no SQLite path — the
// vector type, the cosine-distance operator, and the HNSW index are
// pgvector features — and assumes the "vector" extension already exists
// (docker/backends' init SQL creates it). A namespace column scopes many
// scratchpads to one table (WithMemoryNamespace); the per-request
// Namespace field is issue 1003.
package gormstore
