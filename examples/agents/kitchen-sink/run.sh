#!/usr/bin/env bash
# Launch the kitchen-sink demo: preflight the backends, verify the MCP servers
# are up (started separately via `just servers-up`), then start agentchat with
# every feature wired — durable postgres sessions, tool-result offloading,
# semantic pgvector memory, compaction, OTel traces, and two sub-agent personas
# (from kitchen-sink.json).
#
# Every knob is an env var with a just default, so `just run` and
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

# The agent does NOT manage the MCP servers (root CONSTRAINTS.md: server
# lifecycle is decoupled from the agent). They are brought up independently
# with `just servers-up` and survive chat restarts. Here we only CHECK the four
# ports the config expects and, if any are down, point the operator at the
# command to start them — rather than booting (and killing) them ourselves.
down=""
for probe in demo:8788 runbooks:8789 community:8790 events:8791; do
	name="${probe%%:*}"; port="${probe##*:}"
	# The probe opens (and closes) fd 3 inside a subshell; its exit status is
	# the connect result. Don't close fd 3 in this shell — it was never opened
	# here, so `exec 3>&-` would error and falsely flag the server down.
	if ! (exec 3<>"/dev/tcp/localhost/$port") 2>/dev/null; then
		down="$down $name(:$port)"
	fi
done
if [ -n "$down" ]; then
	echo "MCP server(s) not reachable:$down" >&2
	echo "  -> start them first:  just servers-up      (they run independently of the chat)" >&2
	echo "     or just one:        just servers-up demo" >&2
	# Until the agent connects asynchronously (idea 2), a down server fails the
	# launch, so stop here with a clear message instead of an opaque connect error.
	exit 1
fi

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
