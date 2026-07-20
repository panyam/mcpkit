-- Runs once, on a fresh Postgres volume, as the postgres superuser against the
-- default DB (events). Sets up what the agent stack needs:
--   * the pgvector `vector` extension (semantic MemoryStore, issue 1019)
--   * a dedicated `agent` DB for the durable agent backends (RunStore /
--     ToolResultStore / MemoryStore), separate from the events lib's `events` DB
-- with the extension available in both.
--
-- NOTE: /docker-entrypoint-initdb.d scripts run ONLY when the data directory is
-- empty. On an already-initialized ./data/postgres this file is skipped — reset
-- with `docker compose down -v`, or create the extension manually.

CREATE EXTENSION IF NOT EXISTS vector;

CREATE DATABASE agent;
\connect agent
CREATE EXTENSION IF NOT EXISTS vector;
