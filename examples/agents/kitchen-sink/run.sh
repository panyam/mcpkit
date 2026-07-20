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
ACTIVE="${ACTIVE:-}"
OFFLOAD_THRESHOLD="${OFFLOAD_THRESHOLD:-1024}"
# EMBED_MODEL empty => the embedder comes from kitchen-sink.json's `embedder`
# connection role (a cloud embedding endpoint, no local model needed). Set it
# to force a specific embedder endpoint via flags instead.
EMBED_MODEL="${EMBED_MODEL:-}"
EMBED_URL="${EMBED_URL:-http://localhost:1234/v1}"
EMBED_DIM="${EMBED_DIM:-768}"
EMBED_API_KEY_ENV="${EMBED_API_KEY_ENV:-}"
COMPACT_TOKENS="${COMPACT_TOKENS:-8000}"
EXPORTER="${EXPORTER:-otlp}"
OTLP_ENDPOINT="${OTLP_ENDPOINT:-localhost:4317}"
UI="${UI:-tui}"

bash "$DIR/preflight.sh"

# Redirect the demo server's logs to a file — its stdout/stderr (and mcpkit's
# per-connection SSE logging) share this terminal with agentchat's inline TUI
# and would clobber the input region otherwise. Tail it in another window to
# watch tool calls: tail -f "$SERVER_LOG".
SERVER_LOG="${SERVER_LOG:-${TMPDIR:-/tmp}/kitchen-sink-server.log}"
echo "==> booting kitchen-sink demo server on :8788 (logs -> $SERVER_LOG)"
( cd "$DIR/server" && go run . ) >"$SERVER_LOG" 2>&1 &
SERVER_PID=$!
trap 'kill "$SERVER_PID" 2>/dev/null || true' EXIT

# Wait for the port to accept connections via a TCP probe — NOT an HTTP GET.
# A GET to /mcp opens the server's SSE stream and never returns, so a
# curl-based check would hang the readiness loop (and the launch) forever.
for _ in $(seq 1 60); do
	(exec 3<>/dev/tcp/localhost/8788) 2>/dev/null && { exec 3>&-; break; }
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
	--compact-tokens "$COMPACT_TOKENS"
	--exporter "$EXPORTER"
	--otlp-endpoint "$OTLP_ENDPOINT"
	--ui "$UI"
)
[ -n "$ACTIVE" ] && args+=(--active "$ACTIVE")
# Only override the config's embedder role when EMBED_MODEL is set.
if [ -n "$EMBED_MODEL" ]; then
	args+=(--memory-embed-model "$EMBED_MODEL" --memory-embed-url "$EMBED_URL" --memory-embed-dim "$EMBED_DIM")
	[ -n "$EMBED_API_KEY_ENV" ] && args+=(--memory-embed-api-key-env "$EMBED_API_KEY_ENV")
fi

exec go run . "${args[@]}"
