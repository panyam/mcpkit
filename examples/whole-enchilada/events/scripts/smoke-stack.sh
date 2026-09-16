#!/usr/bin/env bash
# Drives a real MCP session against a running events stack and asserts the
# wire shape the merged design sketch specifies.
#
#   ./scripts/smoke-stack.sh [endpoint]      # default http://localhost:9090/mcp
#
# Used by .github/workflows/publish-images.yml after `docker compose up`, and
# by hand to check a stack you brought up locally. Prints the HTTP status and
# body on any failure, so a red run says what went wrong rather than just
# exiting non-zero.
#
# Speaks the SEP-2575 STATELESS wire: every request carries the namespaced
# a namespaced params._meta envelope (protocolVersion plus the REQUIRED
# clientCapabilities; clientInfo is optional but sent) and there is no
# initialize and no Mcp-Session-Id. nginx round-robins
# across the replicas with no session affinity (see nginx/nginx.conf), so a
# legacy session established on replica 1 is unknown to replicas 2 and 3 on
# the very next request. Stateless is the wire this topology supports, and the
# wire the demo's own clients use.
#
# Deliberately checks nextPollMs rather than merely "a response came back":
# mcpkit shipped nextPollSeconds for four months against a spec that said
# nextPollMs, and every test we had still passed. A smoke test that only
# proved liveness would not have caught it either.
set -euo pipefail

ENDPOINT="${1:-http://localhost:9090/mcp}"
PROTOCOL="${MCP_PROTOCOL_VERSION:-2026-07-28}"

fail() { echo "smoke-stack: FAIL: $*" >&2; exit 1; }

# Emits the response body on stdout. On a non-2xx, reports the status and body
# and exits. Keeps `set -e` from swallowing the one detail worth seeing.
rpc() {
  local method="$1" params_extra="${2:-}"
  local meta="\"io.modelcontextprotocol/protocolVersion\":\"${PROTOCOL}\""
  meta="${meta},\"io.modelcontextprotocol/clientCapabilities\":{}"
  meta="${meta},\"io.modelcontextprotocol/clientInfo\":{\"name\":\"smoke-stack\",\"version\":\"1\"}"
  local params="\"_meta\":{${meta}}"
  [ -n "$params_extra" ] && params="${params},${params_extra}"

  local body status out
  out=$(curl -sS -X POST "$ENDPOINT" \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    -H "MCP-Protocol-Version: ${PROTOCOL}" \
    -w '\n%{http_code}' \
    -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"${method}\",\"params\":{${params}}}") \
    || fail "${method}: curl could not reach ${ENDPOINT}"

  status=$(printf '%s' "$out" | tail -1)
  body=$(printf '%s' "$out" | sed '$d' | grep -v '^$' | tail -1 | sed 's/^data: //')

  if [ "$status" -lt 200 ] || [ "$status" -ge 300 ]; then
    fail "${method}: HTTP ${status}
  body: ${body}"
  fi
  case "$body" in
    *'"error"'*) fail "${method}: JSON-RPC error
  body: ${body}" ;;
  esac
  printf '%s' "$body"
}

echo "smoke-stack: ${ENDPOINT} (stateless wire, protocol ${PROTOCOL})"

LIST=$(rpc "events/list")
echo "$LIST" | grep -q '"chat.message"' \
  || fail "events/list did not advertise chat.message
  body: ${LIST}"
echo "smoke-stack: events/list OK"

POLL=$(rpc "events/poll" '"name":"chat.message"')
echo "$POLL" | grep -q '"nextPollMs"' \
  || fail "events/poll is missing nextPollMs (spec 197c32b4)
  body: ${POLL}"
if echo "$POLL" | grep -q '"nextPollSeconds"'; then
  fail "events/poll still emits the pre-rename nextPollSeconds
  body: ${POLL}"
fi

echo "smoke-stack: events/poll OK -> ${POLL}"
echo "smoke-stack: PASS"
