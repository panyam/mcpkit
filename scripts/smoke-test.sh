#!/bin/bash
# Smoke test for MCP HTTP transports (SSE and Streamable HTTP).
# Starts test servers, runs full MCP lifecycle via curl, and verifies responses.
set -euo pipefail

SSE_PORT=18787
STREAMABLE_PORT=18788
PIDS=()
SSE_FILE=$(mktemp)

cleanup() {
    for pid in "${PIDS[@]}"; do
        kill "$pid" 2>/dev/null || true
    done
    for pid in "${PIDS[@]}"; do
        wait "$pid" 2>/dev/null || true
    done
    rm -f "$SSE_FILE"
}
trap cleanup EXIT

########################################
# SSE TRANSPORT TESTS
########################################
echo "==============================="
echo "=== SSE TRANSPORT TESTS     ==="
echo "==============================="

echo "=== Starting SSE test server on :$SSE_PORT ==="
PORT=$SSE_PORT go run ./cmd/testserver &
PIDS+=($!)
sleep 2

# Open SSE connection in background
curl -s -N "http://localhost:$SSE_PORT/mcp/sse" > "$SSE_FILE" 2>&1 &
PIDS+=($!)
sleep 1

POST_URL=$(grep "^data:" "$SSE_FILE" | head -1 | sed 's/^data: //')
if [ -z "$POST_URL" ]; then
    echo "FAIL: no endpoint URL in SSE stream"
    exit 1
fi
echo "  endpoint URL: $POST_URL"

# Initialize
curl -s -o /dev/null -w "" -X POST "$POST_URL" -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"smoke","version":"1.0"}}}'
sleep 0.2

# Send initialized notification
HTTP_STATUS=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$POST_URL" -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","method":"notifications/initialized"}')
[ "$HTTP_STATUS" = "204" ] && echo "  initialized -> 204 (OK)" || { echo "FAIL: initialized -> $HTTP_STATUS"; exit 1; }

# Call echo tool
curl -s -o /dev/null -X POST "$POST_URL" -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"message":"hello sse"}}}'
sleep 0.2
grep -q 'hello sse' "$SSE_FILE" && echo "  echo tool response on SSE (OK)" || { echo "FAIL: echo response missing"; exit 1; }

# Expired session
HTTP_STATUS=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
    "http://localhost:$SSE_PORT/mcp/message?sessionId=nonexistent" -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","id":1,"method":"ping"}')
[ "$HTTP_STATUS" = "410" ] && echo "  expired session -> 410 (OK)" || { echo "FAIL: expired -> $HTTP_STATUS"; exit 1; }

echo "=== SSE TESTS PASSED ==="
echo ""

########################################
# STREAMABLE HTTP TRANSPORT TESTS
########################################
echo "==============================="
echo "=== STREAMABLE HTTP TESTS   ==="
echo "==============================="

echo "=== Starting Streamable HTTP test server on :$STREAMABLE_PORT ==="
STREAMABLE=1 PORT=$STREAMABLE_PORT go run ./cmd/testserver &
PIDS+=($!)
sleep 2

BASE_URL="http://localhost:$STREAMABLE_PORT/mcp"

# Initialize — extract session ID from header
SESSION_ID=$(curl -s -D - -o /dev/null -X POST "$BASE_URL" \
    -H "Content-Type: application/json" -H "Accept: application/json, text/event-stream" \
    -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"smoke","version":"1.0"}}}' \
    | grep -i "Mcp-Session-Id" | awk '{print $2}' | tr -d '\r')

if [ -z "$SESSION_ID" ]; then
    echo "FAIL: no Mcp-Session-Id header"
    exit 1
fi
echo "  initialize -> session $SESSION_ID (OK)"

# Send initialized notification
HTTP_STATUS=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE_URL" \
    -H "Content-Type: application/json" -H "Mcp-Session-Id: $SESSION_ID" \
    -d '{"jsonrpc":"2.0","method":"notifications/initialized"}')
[ "$HTTP_STATUS" = "202" ] && echo "  initialized -> 202 (OK)" || { echo "FAIL: initialized -> $HTTP_STATUS"; exit 1; }

# Call echo tool — response in HTTP body
ECHO_RESP=$(curl -s -X POST "$BASE_URL" \
    -H "Content-Type: application/json" -H "Mcp-Session-Id: $SESSION_ID" \
    -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"message":"hello streamable"}}}')
echo "$ECHO_RESP" | grep -q 'hello streamable' && echo "  echo tool -> response in body (OK)" || { echo "FAIL: $ECHO_RESP"; exit 1; }

# Missing session header
HTTP_STATUS=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE_URL" \
    -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}')
[ "$HTTP_STATUS" = "400" ] && echo "  missing session -> 400 (OK)" || { echo "FAIL: missing session -> $HTTP_STATUS"; exit 1; }

# Unknown session
HTTP_STATUS=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE_URL" \
    -H "Content-Type: application/json" -H "Mcp-Session-Id: bogus" \
    -d '{"jsonrpc":"2.0","id":1,"method":"ping"}')
[ "$HTTP_STATUS" = "404" ] && echo "  unknown session -> 404 (OK)" || { echo "FAIL: unknown session -> $HTTP_STATUS"; exit 1; }

# DELETE session
HTTP_STATUS=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE "$BASE_URL" \
    -H "Mcp-Session-Id: $SESSION_ID")
[ "$HTTP_STATUS" = "200" ] && echo "  DELETE session -> 200 (OK)" || { echo "FAIL: DELETE -> $HTTP_STATUS"; exit 1; }

# Post-delete should be 404
HTTP_STATUS=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE_URL" \
    -H "Content-Type: application/json" -H "Mcp-Session-Id: $SESSION_ID" \
    -d '{"jsonrpc":"2.0","id":1,"method":"ping"}')
[ "$HTTP_STATUS" = "404" ] && echo "  post-delete -> 404 (OK)" || { echo "FAIL: post-delete -> $HTTP_STATUS"; exit 1; }

echo "=== STREAMABLE HTTP TESTS PASSED ==="
echo ""
echo "=== ALL SMOKE TESTS PASSED ==="
