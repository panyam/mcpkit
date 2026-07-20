#!/usr/bin/env bash
# Preflight for the kitchen-sink demo: probe every backend the harness wires
# and, for anything missing, print the exact command to bring it up. Postgres
# is required (the config points --session-store at it); the observability
# stack and the embedder endpoint are optional but memory/traces degrade
# without them, so they warn rather than fail.
#
# Env (all have Make/just defaults): PG_HOST PG_PORT PG_DB PG_USER
# OTLP_ENDPOINT EMBED_URL EMBED_MODEL EMBED_DIM. Exit non-zero iff a required
# backend is down.
set -uo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$DIR/../../.." && pwd)"

PG_HOST="${PG_HOST:-localhost}"
PG_PORT="${PG_PORT:-5432}"
PG_DB="${PG_DB:-agent}"
PG_USER="${PG_USER:-postgres}"
OTLP_ENDPOINT="${OTLP_ENDPOINT:-localhost:4317}"
EMBED_URL="${EMBED_URL:-http://localhost:1234/v1}"
EMBED_MODEL="${EMBED_MODEL:-text-embedding-nomic-embed-text-v1.5}"
EMBED_DIM="${EMBED_DIM:-768}"

fail=0

tcp_up() { # host port
	(exec 3<>"/dev/tcp/$1/$2") 2>/dev/null && exec 3>&- && return 0
	return 1
}

echo "kitchen-sink preflight"
echo "----------------------"

# --- Postgres + the `agent` DB (required) --------------------------------
if tcp_up "$PG_HOST" "$PG_PORT"; then
	dbok=1
	if command -v docker >/dev/null 2>&1 && docker ps --format '{{.Names}}' 2>/dev/null | grep -q '^mcpkit-postgres$'; then
		docker exec mcpkit-postgres psql -U "$PG_USER" -d "$PG_DB" -c 'select 1' >/dev/null 2>&1 || dbok=0
	fi
	if [ "$dbok" = 1 ]; then
		echo "  [ok]   postgres   $PG_HOST:$PG_PORT (db=$PG_DB)"
	else
		echo "  [FAIL] postgres   reachable, but db '$PG_DB' or pgvector missing"
		echo "         -> the agent DB is created on a FRESH volume only. Reset it:"
		echo "            (cd $ROOT/docker/backends && just down && rm -rf data/postgres && just up)"
		fail=1
	fi
else
	echo "  [FAIL] postgres   $PG_HOST:$PG_PORT unreachable (required for sessions/offload/memory)"
	echo "         -> cd $ROOT/docker/backends && just up      # or: just allup"
	fail=1
fi

# --- OTLP collector (optional) -------------------------------------------
otlp_host="${OTLP_ENDPOINT%%:*}"
otlp_port="${OTLP_ENDPOINT##*:}"
if tcp_up "$otlp_host" "$otlp_port"; then
	echo "  [ok]   otel       $OTLP_ENDPOINT"
else
	echo "  [warn] otel       $OTLP_ENDPOINT unreachable (traces disabled; use EXPORTER=auto to silently skip)"
	echo "         -> cd $ROOT/docker/observability && just up   # or: just allup"
fi

# --- Embedder endpoint (optional, needed for semantic memory) ------------
if command -v curl >/dev/null 2>&1 && curl -fsS -m 3 "$EMBED_URL/models" >/dev/null 2>&1; then
	echo "  [ok]   embedder   $EMBED_URL (model=$EMBED_MODEL, dim=$EMBED_DIM)"
else
	echo "  [warn] embedder   $EMBED_URL unreachable (remember/recall will error; chat still works)"
	echo "         -> start an OpenAI-compatible /embeddings endpoint and load '$EMBED_MODEL', e.g.:"
	echo "            LM Studio: load an embedding model, serve on :1234  (EMBED_URL default)"
	echo "            Ollama:    ollama pull nomic-embed-text && ollama serve"
	echo "                       then EMBED_URL=http://localhost:11434/v1 EMBED_MODEL=nomic-embed-text EMBED_DIM=768"
	echo "         (set EMBED_DIM to the model's true width — pgvector rejects a mismatch)"
fi

echo "----------------------"
if [ "$fail" != 0 ]; then
	echo "preflight: required backend(s) down — bring them up and retry (or 'just allup')." >&2
	exit 1
fi
echo "preflight: ready."
