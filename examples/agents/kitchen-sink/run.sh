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

# The config connects to four MCP servers (kitchen-sink.json):
#   demo      :8788  greet/report/analyze (offloading + sub-agent tools)
#   runbooks  :8789  skills-core, EAGER   (full skill bodies in the prompt)
#   community :8790  skills,      CATALOG (bodies fetched on demand via load_skill)
#   events    :8791  synthetic chat.message + alert.fired (feed injection)
# The host is a pure client — it CONNECTS to these, it does not manage them, so
# the launcher boots them here on the ports the config expects. Each server's
# logs go to their own file: their stdout/stderr (and mcpkit's per-connection
# SSE logging) would otherwise clobber agentchat's inline TUI input region.
# Tail one in another window to watch it: tail -f "$LOG_DIR/kitchen-sink-<name>.log".
LOG_DIR="${LOG_DIR:-${TMPDIR:-/tmp}}"
SERVER_PIDS=()
trap 'for p in "${SERVER_PIDS[@]:-}"; do kill "$p" 2>/dev/null || true; done' EXIT

# boot_server <label> <workdir> <port> <run-cmd...>
# Boots a server in the background with its logs redirected, then waits for the
# port via a TCP probe — NOT an HTTP GET: a GET to /mcp opens the server's SSE
# stream and never returns, so a curl-based check would hang the launch forever.
boot_server() {
	local label="$1" workdir="$2" port="$3"; shift 3
	local log="$LOG_DIR/kitchen-sink-$label.log"
	echo "==> booting $label on :$port (logs -> $log)"
	( cd "$workdir" && "$@" ) >"$log" 2>&1 &
	SERVER_PIDS+=("$!")
	for _ in $(seq 1 60); do
		(exec 3<>"/dev/tcp/localhost/$port") 2>/dev/null && { exec 3>&-; return 0; }
		sleep 0.5
	done
	echo "!! $label did not come up on :$port — see $log" >&2
	return 1
}

boot_server demo      "$DIR/server"                       8788 go run .
boot_server runbooks  "$ROOT/examples/skills-core"        8789 go run . --serve --addr=:8789
boot_server community "$ROOT/examples/skills"             8790 go run . --serve --addr=:8790
boot_server events    "$ROOT/examples/events/kitchen-sink" 8791 go run . --serve --addr=:8791

echo "==> launching agentchat (session=$SESSION, store=$SESSION_STORE)"
cd "$ROOT/cmd/agentchat"
args=(
	--config "$DIR/kitchen-sink.json"
	--persist-config
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
