#!/usr/bin/env bash
# Drives a real MCP session against a running events stack and asserts the
# wire shape the merged design sketch specifies.
#
#   ./scripts/smoke-stack.sh [endpoint]          # anonymous stack
#   EXPECT_AUTH=1 ./scripts/smoke-stack.sh [..]  # auth stack: assert refusal
#
# Used by .github/workflows/publish-images.yml after `docker compose up`, and
# by hand via `make smoke` (dev stack, auth on), `make stack-smoke` (published
# stack, anonymous) or `make stack-smoke-auth` (published stack, auth profile).
#
# Speaks the SEP-2575 STATELESS wire: every request carries a namespaced
# params._meta envelope (protocolVersion plus the REQUIRED clientCapabilities)
# and there is no initialize and no Mcp-Session-Id. nginx round-robins across
# the replicas with no session affinity, so a legacy session established on
# replica 1 is unknown to replicas 2 and 3 on the very next request.
#
# The two modes assert opposite things about the SAME first request, which is
# why they share one code path. Against the default stack an anonymous caller
# must be served. Against the auth profile it must be refused. Running the
# anonymous assertions against an auth stack is what made an earlier version
# of this script report a confusing 401 mid-run.
#
# Deliberately checks nextPollMs rather than merely "a response came back":
# mcpkit shipped nextPollSeconds for four months against a spec that said
# nextPollMs, and every test we had still passed.
set -euo pipefail

ENDPOINT="${1:-http://localhost:9090/mcp}"
PROTOCOL="${MCP_PROTOCOL_VERSION:-2026-07-28}"
EXPECT_AUTH="${EXPECT_AUTH:-}"

fail() { echo "smoke-stack: FAIL: $*" >&2; exit 1; }

# An events/list body is a few KB of JSON Schema. Printing it whole buries the
# line that matters, so failures show a head and say how much was cut.
brief() {
  local s="$1" max=360
  if [ "${#s}" -le "$max" ]; then printf '%s' "$s"; else
    printf '%s... [%d more chars]' "${s:0:$max}" "$(( ${#s} - max ))"
  fi
}

STATUS=""
BODY=""

# call sets STATUS and BODY. It never fails on a non-2xx, because both modes
# need to inspect the refusal rather than abort on it.
call() {
  local method="$1" extra="${2:-}"
  local meta="\"io.modelcontextprotocol/protocolVersion\":\"${PROTOCOL}\",\"io.modelcontextprotocol/clientCapabilities\":{}"
  local params="\"_meta\":{${meta}}"
  [ -n "$extra" ] && params="${params},${extra}"

  local out
  out=$(curl -sS --max-time 20 -X POST "$ENDPOINT" \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    -H "MCP-Protocol-Version: ${PROTOCOL}" \
    -w '\n%{http_code}' \
    -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"${method}\",\"params\":{${params}}}") \
    || fail "${method}: curl could not reach ${ENDPOINT}"

  STATUS=$(printf '%s' "$out" | tail -1)
  BODY=$(printf '%s' "$out" | sed '$d' | grep -v '^$' | tail -1 | sed 's/^data: //')

  # A legacy-wire answer means the envelope did not route as intended, and any
  # assertion built on it proves nothing either way.
  case "$BODY" in
    *"missing Mcp-Session-Id"*)
      fail "${method}: request landed on the legacy wire, so this run proved nothing.
  The _meta envelope is malformed or not nested under params._meta.
  body: $(brief "$BODY")" ;;
  esac
}

refused() {
  case "$STATUS" in 401|403) return 0 ;; esac
  case "$BODY" in *-32012*|*Forbidden*|*forbidden*) return 0 ;; esac
  return 1
}

echo "smoke-stack: ${ENDPOINT} (stateless wire, protocol ${PROTOCOL}${EXPECT_AUTH:+, expecting auth})"

# One request decides everything. Both modes read it, in opposite directions.
call "events/list"

if [ -n "$EXPECT_AUTH" ]; then
  if refused; then
    echo "smoke-stack: anonymous events/list refused (HTTP ${STATUS})"
    echo "smoke-stack: PASS"
    exit 0
  fi
  fail "auth was expected but an anonymous events/list was SERVED (HTTP ${STATUS}).
  The replicas are running with OAUTH_INTROSPECTION_URLS empty. Compose
  reuses a container whose config has not changed, so starting Keycloak
  alone does not switch them onto introspection. Try:
    make down && make up                       (dev stack)
    make stack-down && make stack-up-auth      (published stack)
  body: $(brief "$BODY")"
fi

if refused; then
  fail "events/list was refused (HTTP ${STATUS}), so this stack has auth on.
  That is what 'make up' brings up. Verify it with:   make smoke
  For the published stack's auth profile:             make stack-smoke-auth
  For an anonymous stack: make stack-down && make stack-up && make stack-smoke
  body: $(brief "$BODY")"
fi
[ "$STATUS" -ge 200 ] && [ "$STATUS" -lt 300 ] \
  || fail "events/list: HTTP ${STATUS}
  body: $(brief "$BODY")"
echo "$BODY" | grep -q '"chat.message"' \
  || fail "events/list did not advertise chat.message
  body: $(brief "$BODY")"
echo "smoke-stack: events/list OK"

call "events/poll" '"name":"chat.message"'
[ "$STATUS" -ge 200 ] && [ "$STATUS" -lt 300 ] \
  || fail "events/poll: HTTP ${STATUS}
  body: $(brief "$BODY")"
case "$BODY" in *'"error"'*) fail "events/poll returned a JSON-RPC error
  body: $(brief "$BODY")" ;; esac

echo "$BODY" | grep -q '"nextPollMs"' \
  || fail "events/poll is missing nextPollMs (spec 197c32b4)
  body: $(brief "$BODY")"
if echo "$BODY" | grep -q '"nextPollSeconds"'; then
  fail "events/poll still emits the pre-rename nextPollSeconds
  body: $(brief "$BODY")"
fi

echo "smoke-stack: events/poll OK -> ${BODY}"
echo "smoke-stack: PASS"
