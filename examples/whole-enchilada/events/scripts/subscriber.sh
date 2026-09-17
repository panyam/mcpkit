#!/usr/bin/env bash
# One implementation behind `poller`, `webhook` and `streamer`. All three take
# the same auth arguments, differ only in which binary they run and which demo
# knobs they accept, and were previously written twice each — once in Make's
# `$(if ...)` and once in just's `{{if ... }}` — for six copies of the same
# TOKEN-or-USERNAME logic. The usage strings had already drifted apart.
#
# Usage: subscriber.sh <poller|webhook|streamer>
#
# Input is env, matching what both runners already passed:
#   TENANT        A|B|C or a literal realm (required in practice)
#   TOKEN         bearer, or
#   USERNAME      with optional PASSWORD (defaults to USERNAME)
#   EVENT         event type to subscribe to
#   START_CURSOR  poller only — 0 replays from the buffer head
#   TTL_MS / REPLY_STATUS / EXIT_AFTER   webhook only
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
. scripts/common.sh

KIND="${1:?internal: subscriber.sh needs poller|webhook|streamer}"

TENANT="${TENANT:-}"
TOKEN="${TOKEN:-}"
USERNAME="${USERNAME:-}"
PASSWORD="${PASSWORD:-}"
EVENT="${EVENT:-chat.message}"
START_CURSOR="${START_CURSOR:-}"
TTL_MS="${TTL_MS:-}"
REPLY_STATUS="${REPLY_STATUS:-}"
EXIT_AFTER="${EXIT_AFTER:-}"

AUTH_ARGS="[TOKEN=<bearer> | USERNAME=<u> [PASSWORD=<p>]]"
case "$KIND" in
    poller)   EXTRA="[START_CURSOR=<N>]" ;;
    webhook)  EXTRA="[TTL_MS=<int|null>] [REPLY_STATUS=<int>] [EXIT_AFTER=<int>]" ;;
    streamer) EXTRA="" ;;
    *) echo "internal: unknown subscriber kind '$KIND'" >&2; exit 2 ;;
esac

if [ -z "$TOKEN" ] && [ -z "$USERNAME" ]; then
    # shellcheck disable=SC2086
    usage_for "$KIND" "TENANT=A|B|C" "$AUTH_ARGS" $EXTRA
fi

args=(--tenant "$(tenant_realm "$TENANT")")
if [ -n "$TOKEN" ]; then
    args+=(--token "$TOKEN")
fi
if [ -n "$USERNAME" ]; then
    args+=(--username "$USERNAME" --password "${PASSWORD:-$USERNAME}")
fi

# Flag order is kept as each recipe had it, so a copied command line from the
# README or the walkthrough still reads the same in the process list.
if [ "$KIND" = "poller" ]; then
    if [ -n "$START_CURSOR" ]; then
        args+=(--start-cursor "$START_CURSOR")
    fi
fi

args+=(--event "$EVENT")

if [ "$KIND" = "webhook" ]; then
    if [ -n "$TTL_MS" ]; then       args+=(--ttl-ms "$TTL_MS"); fi
    if [ -n "$REPLY_STATUS" ]; then args+=(--reply-status "$REPLY_STATUS"); fi
    if [ -n "$EXIT_AFTER" ]; then   args+=(--exit-after "$EXIT_AFTER"); fi
fi

exec go -C "$KIND" run . "${args[@]}"
