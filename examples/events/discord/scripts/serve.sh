#!/usr/bin/env bash
# Starts the discord example server. With no bot token it runs in test mode, which
# is what CI and a first read-through use.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

# NOT $ADDR: in this example ADDR is the client-facing URL
# (http://localhost:8080), not a listen address. The recipe passed :8080
# literally, so SERVE_ADDR is a separate knob.
ADDR="${SERVE_ADDR:-:8080}"
TOKEN="${DISCORD_BOT_TOKEN:-}"

if [ -n "$TOKEN" ]; then
    echo "Starting with Discord bot..."
    exec go run . --serve -addr "$ADDR" -token "$TOKEN"
fi

echo "Starting in test mode (no Discord token)..."
exec go run . --serve -addr "$ADDR"
