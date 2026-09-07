#!/usr/bin/env bash
# SEP-2663 tasks conformance — drives examples/tasks-v2 through every upstream
# `tasks-*` server scenario. Requires zero FAILURE rows.
#
# Previously this ran upstream's src/scenarios/server/all-scenarios.test.ts with
# TASKS_SERVER_URL / TASKS_SERVER_CMD set. Nothing upstream reads those vars and
# that test file spawns its own everything-server.ts, so the stage graded the
# TypeScript reference server and never touched mcpkit. It reported an identical
# pass count with and without conformance PR 487 applied, which is how the gap
# surfaced. The CLI-driven shape below matches conf-stateless.sh and section 4b
# of scripts/conformance-audit.sh, which was the only place tasks were really
# graded.
#
# Runner-agnostic: the Makefile + justfile `testconf-tasks-v2` recipes call this
# directly. REPO_ROOT + CONFORMANCE_DIR + MCPCONFORMANCE_TASKS_V2_PATH resolve
# via _common.sh (path-defaults.sh); override the path via env.
set -u
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_common.sh"

require_conf_dir MCPCONFORMANCE_TASKS_V2_PATH \
    "Clone https://github.com/modelcontextprotocol/conformance there:" \
    "  git clone https://github.com/modelcontextprotocol/conformance.git ${MCPCONFORMANCE_TASKS_V2_PATH}" \
    "Or set MCPCONFORMANCE_TASKS_V2_PATH=<path-to-clone>."

PORT="${TASKS_V2_PORT:-18092}"

build_conf_dist MCPCONFORMANCE_TASKS_V2_PATH

(cd "${REPO_ROOT}/examples/tasks-v2" && go build -o tasks-v2 .) || exit 1

OUT=$(mktemp -d -t conf-tasks-v2.XXXXXX)
echo "Spawning examples/tasks-v2 on :${PORT}, scratch dir $OUT"
STREAMABLE=1 "${REPO_ROOT}/examples/tasks-v2/tasks-v2" --serve --addr ":${PORT}" \
    > "$OUT/server.log" 2>&1 &
PID=$!
trap 'kill $PID 2>/dev/null; wait $PID 2>/dev/null' EXIT

for i in $(seq 1 30); do
    curl -sf -o /dev/null -X POST "http://localhost:${PORT}/mcp" \
        -H "Content-Type: application/json" \
        -H "Accept: application/json, text/event-stream" \
        -d '{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"conf-tasks-v2","version":"0"}}}' \
        2>/dev/null && break
    sleep 1
done

SCENARIOS=$(cd "${MCPCONFORMANCE_TASKS_V2_PATH}" && node dist/index.js list --server 2>/dev/null \
    | awk '/^  - /{print $2}' | grep '^tasks-')
if [ -z "$SCENARIOS" ]; then
    echo "testconf-tasks-v2: upstream listed no tasks-* scenarios"
    exit 1
fi

for scenario in $SCENARIOS; do
    (cd "${MCPCONFORMANCE_TASKS_V2_PATH}" && \
        node dist/index.js server \
            --url "http://localhost:${PORT}/mcp" \
            --scenario "$scenario" \
            --suite all \
            -o "$OUT/checks" >> "$OUT/runner.log" 2>&1)
done

CHECKS=$(find "$OUT/checks" -name checks.json 2>/dev/null)
if [ -z "$CHECKS" ]; then
    echo "testconf-tasks-v2: upstream runner produced no checks.json"
    tail -30 "$OUT/runner.log"
    exit 1
fi

# shellcheck disable=SC2086
FAILS=$(cat $CHECKS | grep -c '"status": "FAILURE"')
# shellcheck disable=SC2086
PASSES=$(cat $CHECKS | grep -c '"status": "SUCCESS"')
# shellcheck disable=SC2086
SKIPS=$(cat $CHECKS | grep -c '"status": "SKIPPED"')
# shellcheck disable=SC2086
WARNS=$(cat $CHECKS | grep -c '"status": "WARNING"')
NSCEN=$(echo "$SCENARIOS" | wc -l | tr -d ' ')
echo "testconf-tasks-v2: $PASSES pass / $FAILS fail / $WARNS warn / $SKIPS skip across $NSCEN scenarios (artifacts: $OUT/checks)"

if [ "$FAILS" -gt 0 ]; then
    echo "Unexpected FAILURE rows:"
    # shellcheck disable=SC2086
    grep -B 4 '"status": "FAILURE"' $CHECKS | head -80
    exit 1
fi

# mcpkit-stricter sentinel (conformance/tasks-v2/) — checks that go beyond what
# the spec mandates. Placeholder today; see that folder's README.
(cd "$CONFORMANCE_DIR" && npm install --silent && npx vitest run tasks-v2/) || exit 1
