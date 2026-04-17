#!/usr/bin/env bash
set -euo pipefail

MCP="${1:-http://localhost:8080/mcp}"

rpc() {
  local sid="$1" body="$2"
  curl -s -N -X POST "$MCP" \
    -H 'Content-Type: application/json' \
    -H "Mcp-Session-Id: $sid" \
    -d "$body" 2>/dev/null | grep '^data: ' | head -1 | sed 's/^data: //'
}

echo "=== Discord Events — Diagnostic ==="
echo ""

echo "--- initialize ---"
INIT_RESP=$(curl -s -D- -X POST "$MCP" \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","clientInfo":{"name":"curl-diag","version":"1.0"},"capabilities":{}}}')

SID=$(echo "$INIT_RESP" | grep -i '^Mcp-Session-Id:' | tr -d '\r\n' | awk '{print $2}')
if [ -z "$SID" ]; then
  echo "ERROR: No session ID. Is the server running?"
  exit 1
fi
echo "Session: $SID"
echo ""

echo "--- notifications/initialized ---"
curl -s -X POST "$MCP" \
  -H 'Content-Type: application/json' \
  -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized"}' > /dev/null
echo "OK"
echo ""

echo "--- tools/list ---"
rpc "$SID" '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' | jq -r '.result.tools[].name'
echo ""

echo "--- resources/list ---"
rpc "$SID" '{"jsonrpc":"2.0","id":3,"method":"resources/list","params":{}}' | jq -r '.result.resources[].uri'
echo ""

echo "--- events/list ---"
rpc "$SID" '{"jsonrpc":"2.0","id":4,"method":"events/list","params":{}}' \
  | jq '.result.events[]'
echo ""

echo "--- events/poll (cursor=0) ---"
rpc "$SID" '{"jsonrpc":"2.0","id":5,"method":"events/poll","params":{"subscriptions":[{"id":"diag","name":"discord.message","cursor":"0"}]}}' \
  | jq '.result.results[0]'
echo ""

echo "--- discord://messages/recent ---"
RECENT=$(rpc "$SID" '{"jsonrpc":"2.0","id":6,"method":"resources/read","params":{"uri":"discord://messages/recent"}}' \
  | jq -r '.result.contents[0].text')
COUNT=$(echo "$RECENT" | jq 'length')
echo "$COUNT messages in store"
if [ "$COUNT" -gt 0 ]; then
  echo "$RECENT" | jq '.[-3:][] | "\(.sender): \(.text)"'
fi
echo ""

echo "=== Done ==="
