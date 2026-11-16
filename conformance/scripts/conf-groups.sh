#!/usr/bin/env bash
# Groups extension conformance (progressive-disclosure-wg PR 19, #1708).
# Drives examples/disclosure --conformance-groups through the fork's `groups`
# server scenario on both wires: the draft (stateless) wire, which extension
# scenarios run on by default, and 2025-11-25 (session) with --force.
#
# The script exits 1 on any FAILURE row, so `make exercise-disclosure` sees a
# regression. testall runs it as an info stage, the way it runs the other
# fork-sourced suites: the scenario lives on panyam/mcpconformance until the
# WG draft settles, and its check IDs use a groups- prefix until the SEP has a
# number.
#
# Runner-agnostic: the Makefile + justfile `testconf-groups` recipes call this
# directly. REPO_ROOT + MCPCONFORMANCE_GROUPS_PATH resolve via _common.sh
# (path-defaults.sh); override the latter via env.
set -u
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_common.sh"

require_conf_dir MCPCONFORMANCE_GROUPS_PATH \
    "Run 'make conf-groups-setup' to create it, or set MCPCONFORMANCE_GROUPS_PATH=<path-to-clone>." \
    "Default expects the feat/groups-conformance branch checked out at ../conf-groups."
build_conf_dist MCPCONFORMANCE_GROUPS_PATH

PORT="${GROUPS_PORT:-18098}"
BIN_DIR=$(mktemp -d -t conf-groups-bin.XXXXXX)
(cd "${REPO_ROOT}/examples/disclosure" && go build -o "${BIN_DIR}/disclosure" .) || exit 1

OUT=$(mktemp -d -t conf-groups.XXXXXX)
echo "Spawning examples/disclosure --conformance-groups on :${PORT}, scratch dir $OUT"
require_port_free "${PORT}"
"${BIN_DIR}/disclosure" --serve --addr=":${PORT}" --conformance-groups > "$OUT/server.log" 2>&1 &
PID=$!
trap 'kill $PID 2>/dev/null; wait $PID 2>/dev/null' EXIT

for i in $(seq 1 30); do
    curl -sf -o /dev/null -X OPTIONS "http://localhost:${PORT}/mcp" && break
    sleep 0.3
done
require_fixture_alive "$PID" "$OUT/server.log"

# The scenario is registered in the pending suite, since the everything-server
# does not implement the extension, so it runs by exact name.
for wire in stateless session; do
    args=""
    [ "$wire" = session ] && args="--spec-version 2025-11-25 --force"
    # shellcheck disable=SC2086
    (cd "${MCPCONFORMANCE_GROUPS_PATH}" && \
        node dist/index.js server \
            --url "http://localhost:${PORT}/mcp" \
            --scenario groups $args \
            -o "$OUT/checks-$wire" >> "$OUT/runner.log" 2>&1)
done

FAILS=0; PASSES=0; SKIPS=0; WARNS=0; SUMMARY=""
for wire in stateless session; do
    CHECKS=$(find "$OUT/checks-$wire" -name checks.json 2>/dev/null)
    if [ -z "$CHECKS" ]; then
        echo "testconf-groups: the runner produced no checks.json on the $wire wire"
        tail -30 "$OUT/runner.log"
        exit 1
    fi
    # shellcheck disable=SC2086
    f=$(cat $CHECKS | grep -c '"status": "FAILURE"'); p=$(cat $CHECKS | grep -c '"status": "SUCCESS"')
    # shellcheck disable=SC2086
    s=$(cat $CHECKS | grep -c '"status": "SKIPPED"'); w=$(cat $CHECKS | grep -c '"status": "WARNING"')
    FAILS=$((FAILS+f)); PASSES=$((PASSES+p)); SKIPS=$((SKIPS+s)); WARNS=$((WARNS+w))
    SUMMARY="$SUMMARY $wire $p/$((p+f+w))"
done
echo "testconf-groups: $PASSES pass / $FAILS fail / $WARNS warn / $SKIPS skip,$SUMMARY (artifacts: $OUT)"

if [ "$FAILS" -gt 0 ]; then
    echo "Unexpected FAILURE rows:"
    grep -h -B 4 '"status": "FAILURE"' "$OUT"/checks-*/*/checks.json "$OUT"/checks-*/checks.json 2>/dev/null | head -80
    exit 1
fi
