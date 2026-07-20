#!/usr/bin/env bash
# Launch the kitchen-sink demo: preflight the backends, boot the demo MCP
# server, then start agentchat with every feature wired — durable postgres
# sessions, tool-result offloading, semantic pgvector memory, compaction,
# OTel traces, and two sub-agent personas (from kitchen-sink.json).
#
# Every knob is an env var with a Make/just default, so `just run` and
# `SESSION=foo EMBED_DIM=1536 just run` both work. The chat model comes from
# kitchen-sink.json's connections block; only the embedder is set by flag.
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$DIR/../../.." && pwd)"

SESSION_STORE="${SESSION_STORE:-postgres://postgres:postgres@localhost:5432/agent}"
SESSION="${SESSION:-kitchen-sink}"
OFFLOAD_THRESHOLD="${OFFLOAD_THRESHOLD:-1024}"
EMBED_MODEL="${EMBED_MODEL:-text-embedding-nomic-embed-text-v1.5}"
EMBED_URL="${EMBED_URL:-http://localhost:1234/v1}"
EMBED_DIM="${EMBED_DIM:-768}"
EMBED_API_KEY_ENV="${EMBED_API_KEY_ENV:-}"
COMPACT_TOKENS="${COMPACT_TOKENS:-8000}"
EXPORTER="${EXPORTER:-otlp}"
OTLP_ENDPOINT="${OTLP_ENDPOINT:-localhost:4317}"
UI="${UI:-tui}"

bash "$DIR/preflight.sh"

echo "==> booting kitchen-sink demo server on :8788"
( cd "$DIR/server" && go run . ) &
SERVER_PID=$!
trap 'kill "$SERVER_PID" 2>/dev/null || true' EXIT

for _ in $(seq 1 30); do
	if curl -s -o /dev/null http://localhost:8788/mcp 2>/dev/null; then break; fi
	sleep 0.5
done

echo "==> launching agentchat (session=$SESSION, store=$SESSION_STORE)"
cd "$ROOT/cmd/agentchat"
args=(
	--config "$DIR/kitchen-sink.json"
	--session-store "$SESSION_STORE"
	--session "$SESSION"
	--offload-threshold "$OFFLOAD_THRESHOLD"
	--memory --memory-inject-recall
	--memory-embed-model "$EMBED_MODEL"
	--memory-embed-url "$EMBED_URL"
	--memory-embed-dim "$EMBED_DIM"
	--compact-tokens "$COMPACT_TOKENS"
	--exporter "$EXPORTER"
	--otlp-endpoint "$OTLP_ENDPOINT"
	--ui "$UI"
)
[ -n "$EMBED_API_KEY_ENV" ] && args+=(--memory-embed-api-key-env "$EMBED_API_KEY_ENV")

exec go run . "${args[@]}"
