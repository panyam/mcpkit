#!/usr/bin/env bash
# Drives a real MCP session against a running events stack and asserts the
# wire shape the merged design sketch specifies.
#
#   ./scripts/smoke-stack.sh [endpoint]      # default http://localhost:9090/mcp
#
# Used by .github/workflows/publish-images.yml after `docker compose up`, and
# by hand to check a stack you brought up locally. Exits non-zero with the
# offending payload on any mismatch, so it is safe to chain after `up`.
#
# Deliberately checks nextPollMs rather than merely "a response came back":
# mcpkit shipped nextPollSeconds for four months against a spec that said
# nextPollMs, and every test we had still passed. A smoke test that only
# proves liveness would not have caught it either.
set -euo pipefail

ENDPOINT="${1:-http://localhost:9090/mcp}"
ACCEPT='application/json, text/event-stream'

fail() { echo "smoke-stack: $*" >&2; exit 1; }

# The response may arrive as a bare JSON body or as an SSE frame depending on
# how the server negotiated the request, so strip a leading `data: ` if present
# and keep the last non-empty line.
last_json() { grep -v '^$' | tail -1 | sed 's/^data: //'; }

echo "smoke-stack: initializing against ${ENDPOINT}"
HEADERS=$(mktemp)
trap 'rm -f "$HEADERS"' EXIT
curl -sf -X POST "$ENDPOINT" \
  -H 'Content-Type: application/json' -H "Accept: ${ACCEPT}" \
  -D "$HEADERS" -o /dev/null \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"smoke","version":"1"}}}' \
  || fail "initialize failed against ${ENDPOINT}"

SID=$(tr -d '\r' < "$HEADERS" | awk 'tolower($1)=="mcp-session-id:"{print $2}')
[ -n "$SID" ] || fail "no mcp-session-id header returned by initialize"
echo "smoke-stack: session ${SID}"

curl -sf -X POST "$ENDPOINT" \
  -H 'Content-Type: application/json' -H "Accept: ${ACCEPT}" -H "mcp-session-id: ${SID}" \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized"}' >/dev/null

LIST=$(curl -sf -X POST "$ENDPOINT" \
  -H 'Content-Type: application/json' -H "Accept: ${ACCEPT}" -H "mcp-session-id: ${SID}" \
  -d '{"jsonrpc":"2.0","id":2,"method":"events/list"}' | last_json)
echo "$LIST" | grep -q '"chat.message"' \
  || fail "events/list did not advertise chat.message: ${LIST}"
echo "smoke-stack: events/list OK"

POLL=$(curl -sf -X POST "$ENDPOINT" \
  -H 'Content-Type: application/json' -H "Accept: ${ACCEPT}" -H "mcp-session-id: ${SID}" \
  -d '{"jsonrpc":"2.0","id":3,"method":"events/poll","params":{"name":"chat.message"}}' | last_json)

echo "$POLL" | grep -q '"nextPollMs"' \
  || fail "events/poll response is missing nextPollMs (spec 197c32b4): ${POLL}"
echo "$POLL" | grep -q '"nextPollSeconds"' \
  && fail "events/poll still emits the pre-rename nextPollSeconds: ${POLL}"

echo "smoke-stack: events/poll OK -> ${POLL}"
echo "smoke-stack: PASS"
