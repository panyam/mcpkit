#!/usr/bin/env bash
# Launch the supervisor host (agent/surfaces/chat) against deep-agent-supervisor.json.
# The roster server must already be up on :8795 (start it with `just serve` in
# another terminal) — the agent does not manage MCP-server lifecycle (root
# CONSTRAINTS.md). The specialists are NOT declared in the config: the host
# discovers them from the server's advertised roster (agents/list) and exposes
# each as a delegate tool.
#
# EXPORTER=otlp OTLP_ENDPOINT=localhost:4317 just serve + just demo emits the
# agents.list / agents.get discovery spans, the host-side agents.resolve span,
# and each delegated specialist's Runner spans into your collector.
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$DIR/../../.." && pwd)"

ACTIVE="${ACTIVE:-}"
EXPORTER="${EXPORTER:-}"
OTLP_ENDPOINT="${OTLP_ENDPOINT:-localhost:4317}"
UI="${UI:-tui}"

# TCP-probe the roster server. Never curl GET /mcp — a GET opens the server's
# SSE stream and never returns, which would hang the launcher forever.
if ! (exec 3<>"/dev/tcp/localhost/8795") 2>/dev/null; then
	echo "roster server not reachable on :8795" >&2
	echo "  -> start it first (separate terminal):  just serve" >&2
	exit 1
fi

echo "==> launching supervisor (config=deep-agent-supervisor.json, active=${ACTIVE:-<config default>})"
cd "$ROOT/agent/surfaces/chat"
args=(--config "$DIR/deep-agent-supervisor.json" --ui "$UI")
[ -n "$EXPORTER" ] && args+=(--exporter "$EXPORTER" --otlp-endpoint "$OTLP_ENDPOINT")
[ -n "$ACTIVE" ] && args+=(--active "$ACTIVE")

exec go run . "${args[@]}"
