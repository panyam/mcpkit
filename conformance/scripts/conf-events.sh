#!/usr/bin/env bash
# MCP Events conformance — informational (exit 0 regardless of check results).
# Drives examples/events/kitchen-sink via the fork's scenario runner.
#
# INFO rather than a gate for two reasons. The suite scores against the merged
# design sketch in modelcontextprotocol/experimental-ext-triggers-events, which
# is a design document rather than a ratified spec and has no SEP number yet, so
# the check IDs still carry the placeholder `sep-9999-` prefix. And mcpkit has
# known divergences from that document (issue 1380), so a red run here is the
# suite doing its job rather than a regression. Flip this to a gate once 1380
# closes and the spec text stabilises.
#
# Runner-agnostic: the Makefile + justfile `testconf-events` recipes call this
# directly. REPO_ROOT + MCPCONFORMANCE_EVENTS_PATH resolve via _common.sh
# (path-defaults.sh); override the latter via env.
set -u
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_common.sh"

require_conf_dir MCPCONFORMANCE_EVENTS_PATH \
    "Clone https://github.com/panyam/mcpconformance there or set MCPCONFORMANCE_EVENTS_PATH=<path-to-clone>." \
    "Default expects the feat/events-conformance-suite branch checked out at ../conf-events."
build_conf_dist MCPCONFORMANCE_EVENTS_PATH

(cd "${REPO_ROOT}/examples/events/kitchen-sink" && go build -o kitchen-sink-events .)

OUT=$(mktemp -d -t conf-events.XXXXXX)
echo "Spawning fixture on :18101, scratch dir $OUT"

# The synthetic feeders are left at their defaults. They emit on a 2-4s
# cadence, which is what gives the EventOccurrence checks something real to
# grade; a silent fixture reports them untestable instead, which is correct but
# tells us less.
# --conformance-events registers the diagnostic control tools and the
# poll-and-push-only event type. Without it several rows describe conditions a
# healthy server never produces during a run — an upstream failure, a retention
# gap, a delivery mode a type does not offer — and report untestable, which the
# suite renders red. The flag is off by default because kitchen-sink is a
# published example first; see examples/CONVENTIONS.md § Conformance fixtures.
"${REPO_ROOT}/examples/events/kitchen-sink/kitchen-sink-events" \
    --serve -addr=:18101 --conformance-events > "$OUT/server.log" 2>&1 &
PID=$!
for i in 1 2 3 4 5 6 7 8 9 10; do
    curl -sf -o /dev/null -X OPTIONS http://localhost:18101/mcp && break
    sleep 0.3
done

# Four of the suite's five scenarios. events-webhook-delivery is deliberately
# absent: it needs a callback the fixture can reach, the harness serves one on
# loopback, and kitchen-sink only accepts that because it sets
# WithWebhookAllowPrivateNetworks(true) for `make demo`. Running it here would
# park two SSRF rows permanently red for a reason that is fixture
# configuration rather than a library defect, which is the kind of red people
# learn to scroll past. Tracked separately.
EVENTS_SCENARIOS="events-discovery events-poll events-push events-webhook"
RC=0
for S in ${EVENTS_SCENARIOS}; do
    # events-push watches an idle heartbeat to grade the cadence rows, so it
    # needs longer than the runner's default or those rows report untestable.
    # The others are request/response and finish in seconds.
    TIMEOUT_ARGS=""
    [ "${S}" = "events-push" ] && TIMEOUT_ARGS="--timeout 60000"
    # shellcheck disable=SC2086 # deliberate word-splitting: empty means no flag
    (cd "${MCPCONFORMANCE_EVENTS_PATH}" && \
        node dist/index.js server \
            --url http://localhost:18101/mcp \
            --scenario "${S}" \
            ${TIMEOUT_ARGS} \
            -o "$OUT/checks-${S}" > "$OUT/runner-${S}.log" 2>&1)
    SRC=$?
    SUMMARY=$(grep -E "Passed:" "$OUT/runner-${S}.log" | tail -1 | sed 's/\x1b\[[0-9;]*m//g')
    echo "  ${S}: ${SUMMARY:-runner exited ${SRC} (see $OUT/runner-${S}.log)}"
    [ ${SRC} -ne 0 ] && RC=${SRC}
done

kill $PID 2>/dev/null
wait $PID 2>/dev/null

if [ $RC -ne 0 ]; then
    echo "==================================================================="
    echo "testconf-events: INFORMATIONAL — a runner invocation exited $RC (artifacts in $OUT)"
    echo "This suite is INFO status in conformance/local-suites.yaml."
    echo "events-discovery and events-poll are green; issue 1380 closed the"
    echo "divergences they used to report. The push and webhook scenarios are"
    echo "expected to report these, tracked in issue 1425:"
    echo "  - push notifications carry no subscriptionId in _meta, so a client"
    echo "    running two subscriptions cannot tell which one an event is for."
    echo "  - events/subscribe accepts an http:// delivery.url, where the"
    echo "    document requires -32602."
    echo "  - events/unsubscribe answers success for a key that was never"
    echo "    subscribed, so a typo reads as a successful teardown."
    echo "Exiting 0 so the umbrella reaches refresh-conformance. See issue 1374."
    echo "==================================================================="
    for S in ${EVENTS_SCENARIOS}; do
        echo "--- ${S} ---"; tail -20 "$OUT/runner-${S}.log" 2>/dev/null
    done
fi
exit 0
