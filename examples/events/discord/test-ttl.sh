#!/usr/bin/env bash
# Drive the Python WebhookSubscription auto-refresh helper against a server
# running with a short TTL. Exits 0 on success, non-zero on failure.
#
# Shared by the discord example Makefile + justfile `test-ttl` recipes. Run
# from the example directory (uses `.` for go build and the local test_ttl.py).
#
# POSIX-only (uses lsof, xargs -r, bash trap, kill -9). On Windows, run the
# server and the Python driver manually:
#   1. Manually kill anything bound to TTL_PORT / TTL_HOOK_PORT
#   2. go run . -addr :18080 -webhook-ttl 3s -unsafe-webhook-ttl-bypass
#   3. python3 test_ttl.py --mcp http://localhost:18080/mcp \
#        --inject-url http://localhost:18080/inject --port 19999 --ttl 3 --duration 8
#
# Env (with defaults):
#   TTL_PORT=18080 TTL_HOOK_PORT=19999 TTL_SECONDS=3 TTL_DURATION=8
set -e

TTL_PORT="${TTL_PORT:-18080}"
TTL_HOOK_PORT="${TTL_HOOK_PORT:-19999}"
TTL_SECONDS="${TTL_SECONDS:-3}"
TTL_DURATION="${TTL_DURATION:-8}"

echo "[test-ttl] killing any existing processes on :${TTL_PORT} / :${TTL_HOOK_PORT}..."
lsof -ti :${TTL_PORT} 2>/dev/null | xargs -r kill -9 2>/dev/null || true
lsof -ti :${TTL_HOOK_PORT} 2>/dev/null | xargs -r kill -9 2>/dev/null || true
sleep 0.3
echo "[test-ttl] starting server on :${TTL_PORT} with -webhook-ttl ${TTL_SECONDS}s..."
go build -o /tmp/discord-events-ttl-bin .
/tmp/discord-events-ttl-bin --serve -addr :${TTL_PORT} -webhook-ttl ${TTL_SECONDS}s -unsafe-webhook-ttl-bypass > /tmp/discord-events-ttl.log 2>&1 &
SERVER_PID=$!
trap "kill -9 $SERVER_PID 2>/dev/null || true; lsof -ti :${TTL_PORT} 2>/dev/null | xargs -r kill -9 2>/dev/null || true; rm -f /tmp/discord-events-ttl-bin" EXIT
for i in 1 2 3 4 5 6 7 8 9 10; do
  if curl -sf -o /dev/null http://localhost:${TTL_PORT}/mcp -X POST -H "Content-Type: application/json" -d "{}" 2>/dev/null || nc -z localhost ${TTL_PORT} 2>/dev/null; then
    break
  fi
  sleep 0.2
done
echo "[test-ttl] running driver (duration ${TTL_DURATION}s)..."
python3 test_ttl.py \
  --mcp http://localhost:${TTL_PORT}/mcp \
  --inject-url http://localhost:${TTL_PORT}/inject \
  --port ${TTL_HOOK_PORT} \
  --ttl ${TTL_SECONDS} \
  --duration ${TTL_DURATION}
