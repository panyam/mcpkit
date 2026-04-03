#!/bin/bash
# Smoke test for the MCP HTTP+SSE transport.
# Starts the test server, runs a full MCP lifecycle via curl, and verifies responses.
set -euo pipefail

PORT=18787
SERVER_PID=""
SSE_PID=""
SSE_FILE=$(mktemp)

cleanup() {
    [ -n "$SSE_PID" ] && kill "$SSE_PID" 2>/dev/null || true
    [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null || true
    wait "$SSE_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
    rm -f "$SSE_FILE"
}
trap cleanup EXIT

echo "=== Starting test server on :$PORT ==="
PORT=$PORT go run ./cmd/testserver &
SERVER_PID=$!
sleep 2

if ! kill -0 "$SERVER_PID" 2>/dev/null; then
    echo "FAIL: server failed to start"
    exit 1
fi

echo "=== Step 1: Connect SSE (background), extract session URL ==="
# Keep SSE connection open in background, write output to temp file
curl -s -N "http://localhost:$PORT/mcp/sse" > "$SSE_FILE" 2>&1 &
SSE_PID=$!
sleep 1

POST_URL=$(grep "^data:" "$SSE_FILE" | head -1 | sed 's/^data: //')
if [ -z "$POST_URL" ]; then
    echo "FAIL: no endpoint URL in SSE stream"
    cat "$SSE_FILE"
    exit 1
fi
echo "  POST URL: $POST_URL"

echo "=== Step 2: Initialize ==="
HTTP_STATUS=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$POST_URL" \
    -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"smoke-test","version":"1.0"}}}')

if [ "$HTTP_STATUS" != "202" ]; then
    echo "FAIL: initialize returned HTTP $HTTP_STATUS, want 202"
    exit 1
fi
echo "  initialize -> $HTTP_STATUS (OK)"
sleep 0.2

# Verify initialize response came on SSE stream
if ! grep -q '"protocolVersion"' "$SSE_FILE"; then
    echo "FAIL: no initialize response on SSE stream"
    cat "$SSE_FILE"
    exit 1
fi
echo "  initialize response received on SSE (OK)"

echo "=== Step 3: Send initialized notification ==="
HTTP_STATUS=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$POST_URL" \
    -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","method":"notifications/initialized"}')

if [ "$HTTP_STATUS" != "204" ]; then
    echo "FAIL: initialized notification returned HTTP $HTTP_STATUS, want 204"
    exit 1
fi
echo "  notifications/initialized -> $HTTP_STATUS (OK)"

echo "=== Step 4: List tools ==="
HTTP_STATUS=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$POST_URL" \
    -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}')

if [ "$HTTP_STATUS" != "202" ]; then
    echo "FAIL: tools/list returned HTTP $HTTP_STATUS, want 202"
    exit 1
fi
echo "  tools/list -> $HTTP_STATUS (OK)"
sleep 0.2

if ! grep -q '"echo"' "$SSE_FILE"; then
    echo "FAIL: tools/list response missing echo tool"
    cat "$SSE_FILE"
    exit 1
fi
echo "  tools/list response contains echo tool (OK)"

echo "=== Step 5: Call echo tool ==="
HTTP_STATUS=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$POST_URL" \
    -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"message":"hello from smoke test"}}}')

if [ "$HTTP_STATUS" != "202" ]; then
    echo "FAIL: tools/call returned HTTP $HTTP_STATUS, want 202"
    exit 1
fi
echo "  tools/call echo -> $HTTP_STATUS (OK)"
sleep 0.2

if ! grep -q 'hello from smoke test' "$SSE_FILE"; then
    echo "FAIL: echo response not found on SSE stream"
    cat "$SSE_FILE"
    exit 1
fi
echo "  echo response received on SSE (OK)"

echo "=== Step 6: Call fail tool (test isError semantics) ==="
HTTP_STATUS=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$POST_URL" \
    -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"fail","arguments":{}}}')

if [ "$HTTP_STATUS" != "202" ]; then
    echo "FAIL: tools/call fail returned HTTP $HTTP_STATUS, want 202"
    exit 1
fi
echo "  tools/call fail -> $HTTP_STATUS (OK)"
sleep 0.2

if ! grep -q '"isError":true' "$SSE_FILE"; then
    echo "FAIL: fail tool response missing isError:true"
    cat "$SSE_FILE"
    exit 1
fi
echo "  fail tool returned isError:true (OK)"

echo "=== Step 7: Verify expired session ==="
HTTP_STATUS=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
    "http://localhost:$PORT/mcp/message?sessionId=nonexistent" \
    -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","id":1,"method":"ping"}')

if [ "$HTTP_STATUS" != "410" ]; then
    echo "FAIL: expired session returned HTTP $HTTP_STATUS, want 410"
    exit 1
fi
echo "  expired session -> $HTTP_STATUS (OK)"

echo ""
echo "=== ALL SMOKE TESTS PASSED ==="
