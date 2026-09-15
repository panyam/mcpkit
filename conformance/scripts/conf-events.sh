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
"${REPO_ROOT}/examples/events/kitchen-sink/kitchen-sink-events" \
    --serve -addr=:18101 > "$OUT/server.log" 2>&1 &
PID=$!
for i in 1 2 3 4 5 6 7 8 9 10; do
    curl -sf -o /dev/null -X OPTIONS http://localhost:18101/mcp && break
    sleep 0.3
done

# Phase 1 of the suite ships the discovery and poll scenarios. The push and
# webhook scenarios land later; add them here as they do. Webhook delivery in
# particular needs a callback URL the fixture can reach over https, which this
# localhost setup deliberately cannot provide.
EVENTS_SCENARIOS="events-discovery events-poll"
RC=0
for S in ${EVENTS_SCENARIOS}; do
    (cd "${MCPCONFORMANCE_EVENTS_PATH}" && \
        node dist/index.js server \
            --url http://localhost:18101/mcp \
            --scenario "${S}" \
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
    echo "This suite is INFO status in conformance/local-suites.yaml. Known"
    echo "divergences it is expected to report, all tracked in issue 1380:"
    echo "  - events/list answers while capabilities.events is never declared,"
    echo "    so the surface is unreachable for a spec-following client."
    echo "  - events/poll omits the events key instead of returning [] when"
    echo "    nothing happened."
    echo "  - the events.topology meta-source does not keep the descriptor"
    echo "    contract: delivery null, no inputSchema, and it answers"
    echo "    events/poll despite advertising no poll delivery."
    echo "Exiting 0 so the umbrella reaches refresh-conformance. See issue 1374."
    echo "==================================================================="
    for S in ${EVENTS_SCENARIOS}; do
        echo "--- ${S} ---"; tail -20 "$OUT/runner-${S}.log" 2>/dev/null
    done
fi
exit 0
