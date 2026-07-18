#!/usr/bin/env bash
# Playground launcher (make pg): boots the getting-started demo MCP server
# and launches agentchat's TUI wired to it. Needs a local OpenAI-compatible
# model; see examples/playground/README.md.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DATA="${AGENTCHAT_PG_DIR:-$HOME/.agentchat}"
mkdir -p "$DATA" "$DATA/pg-blobs"

echo "==> booting demo MCP server (getting-started 'greet') on :8787"
( cd "$ROOT/examples/getting-started" && go run ./server ) &
SERVER_PID=$!
trap 'kill "$SERVER_PID" 2>/dev/null || true' EXIT

for _ in $(seq 1 30); do
	if curl -s -o /dev/null http://localhost:8787/mcp 2>/dev/null; then break; fi
	sleep 0.5
done

echo "==> launching agentchat playground (TUI). Edit examples/playground/playground.json for your model."
cd "$ROOT/cmd/agentchat"
go run . \
	--config "$ROOT/examples/playground/playground.json" \
	--session-store "sqlite://$DATA/pg.db" \
	--offload-dir "$DATA/pg-blobs" \
	--offload-threshold 4096 \
	--ui tui
