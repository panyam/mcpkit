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
# EMBED_MODEL empty (the default) => the embedder is resolved from the
# config's `embedder` role below; non-empty means a flag override.
EMBED_URL="${EMBED_URL:-http://localhost:1234/v1}"
EMBED_MODEL="${EMBED_MODEL:-}"
EMBED_DIM="${EMBED_DIM:-}"

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

# --- Embedder (optional, needed for semantic memory) ---------------------
# EMBED_MODEL set => a flag override (probe EMBED_URL). Otherwise the embedder
# comes from kitchen-sink.json's `embedder` role: resolve its endpoint + key
# env from the config so we check the right place.
emb_url="$EMBED_URL"; emb_key_env=""
if [ -z "$EMBED_MODEL" ] && command -v jq >/dev/null 2>&1; then
	cfg="$DIR/kitchen-sink.json"
	ename="$(jq -r '.connections.embedder // empty' "$cfg")"
	if [ -n "$ename" ]; then
		econn="$(jq -c --arg n "$ename" '.connections.connections[$n] // {}' "$cfg")"
		EMBED_MODEL="$(jq -r '.model // empty' <<<"$econn")"
		emb_key_env="$(jq -r '.apiKeyEnv // empty' <<<"$econn")"
		emb_url="$(jq -r '.baseUrl // empty' <<<"$econn")"
		if [ -z "$emb_url" ]; then
			case "$(jq -r '.type // empty' <<<"$econn")" in
				openai)   emb_url="https://api.openai.com/v1" ;;
				gemini)   emb_url="https://generativelanguage.googleapis.com/v1beta/openai" ;;
				lmstudio) emb_url="http://localhost:1234/v1" ;;
				ollama)   emb_url="http://localhost:11434/v1" ;;
			esac
		fi
	fi
fi

if [ -n "$emb_key_env" ] && [ -z "${!emb_key_env:-}" ]; then
	echo "  [warn] embedder   config role needs \$$emb_key_env set (semantic memory will error)"
	echo "         -> export $emb_key_env=...   (the $EMBED_MODEL embedder at $emb_url)"
elif command -v curl >/dev/null 2>&1 && curl -fsS -m 3 "$emb_url/models" >/dev/null 2>&1; then
	echo "  [ok]   embedder   $emb_url (model=$EMBED_MODEL)"
else
	echo "  [warn] embedder   $emb_url not verified (remember/recall may error; chat still works)"
	echo "         -> for a cloud embedder set the provider key (e.g. export OPENAI_API_KEY=...);"
	echo "            for a local one run LM Studio (:1234) or Ollama and set EMBED_MODEL/EMBED_URL/EMBED_DIM"
	echo "         (EMBED_DIM / the config 'dim' must match the model's width — pgvector rejects a mismatch)"
fi

echo "----------------------"
if [ "$fail" != 0 ]; then
	echo "preflight: required backend(s) down — bring them up and retry (or 'just allup')." >&2
	exit 1
fi
echo "preflight: ready."
