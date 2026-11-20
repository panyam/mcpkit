#!/usr/bin/env bash
# The exercise for mission #1737: read examples/events/kitchen-sink's Server
# Card the way a host does before connecting, then connect and check the card
# against what the server reports, once per wire.
#
#   make exercise-server-card
#
# Each wire starts kitchen-sink on a free port and runs
# tools/check-server-card against it. On the legacy wire it also runs the
# events check-webhook-identity command: a webhook receiver verifies a
# delivery with the key from the card, and a card reached through a
# cross-origin redirect is refused (#1738). Exits 1 when any check fails.
set -u
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK=$(mktemp -d -t exercise-server-card.XXXXXX)
BIN="$WORK/kitchen-sink"
PID=""
cleanup() { [ -n "$PID" ] && kill "$PID" 2>/dev/null; wait 2>/dev/null; }
trap cleanup EXIT

(cd "$ROOT/examples/events/kitchen-sink" && go build -o "$BIN" .) || { echo "could not build kitchen-sink"; exit 1; }

free_port() { python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()'; }

FAILED=0
for WIRE in legacy stateless; do
    PORT=$(free_port)
    "$BIN" --serve --addr="127.0.0.1:$PORT" --wire="$WIRE" --webhook-signing-key=ephemeral --chat-every=500ms >"$WORK/server-$WIRE.log" 2>&1 &
    PID=$!
    for _ in $(seq 1 50); do
        curl -s -o /dev/null "http://127.0.0.1:$PORT/mcp/server-card" && break
        sleep 0.1
    done
    echo "=== wire: $WIRE ==="
    (cd "$ROOT" && go run ./tools/check-server-card -url "http://127.0.0.1:$PORT/mcp" -wire "$WIRE") || FAILED=1
    if [ "$WIRE" = legacy ]; then
        (cd "$ROOT/experimental/ext/events/clients/go" && go run ./cmd/check-webhook-identity -url "http://127.0.0.1:$PORT/mcp") || FAILED=1
    fi
    kill "$PID" 2>/dev/null; wait "$PID" 2>/dev/null; PID=""
done

echo ""
echo "=== exercise-server-card @ $(git -C "$ROOT" rev-parse --short HEAD) ==="
[ "$FAILED" -eq 0 ] && echo "  card and webhook identity checks: pass" || echo "  checks FAILED, see above"
echo "Server logs in $WORK"
exit "$FAILED"
