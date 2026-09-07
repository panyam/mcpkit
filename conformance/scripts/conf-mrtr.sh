#!/usr/bin/env bash
# SEP-2322 MRTR conformance — drives cmd/testserver through every upstream
# `input-required-result-*` server scenario, then runs upstream's negative
# suite and the mcpkit-local sentinel.
#
# The fixture is cmd/testserver, not examples/mrtr. The upstream scenarios call
# `test_input_required_result_*` tools that only cmd/testserver/conformance_input_required.go
# registers; examples/mrtr is a walkthrough and answers all fourteen with
# "unknown tool". This turns the informational audit sweep into a hard gate for
# the SEP-2322 surface.
#
# The negative suite (src/scenarios/server/negative-mrtr.test.ts) spawns its own
# deliberately-broken fixture and never contacts mcpkit. It is kept because it
# verifies the upstream checks still emit FAILURE against a bad server, which
# guards against a vacuously-green harness. It used to be the only thing this
# target ran, with MRTR_SERVER_URL / MRTR_SERVER_CMD set; nothing upstream reads
# those vars, so no mcpkit code was graded here at all. Same wiring gap as the
# one fixed in conf-tasks-v2.sh.
#
# Runner-agnostic: the Makefile + justfile `testconf-mrtr` recipes call this
# directly. REPO_ROOT + CONFORMANCE_DIR + MCPCONFORMANCE_MRTR_PATH resolve via
# _common.sh (path-defaults.sh); override the path via env.
set -u
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_common.sh"

require_conf_dir MCPCONFORMANCE_MRTR_PATH \
    "Clone https://github.com/modelcontextprotocol/conformance there or set MCPCONFORMANCE_MRTR_PATH=<path-to-clone>."

PORT="${MRTR_PORT:-18093}"

build_conf_dist MCPCONFORMANCE_MRTR_PATH

(cd "${REPO_ROOT}" && go build -o bin/testserver ./cmd/testserver) || exit 1

OUT=$(mktemp -d -t conf-mrtr.XXXXXX)
echo "Spawning cmd/testserver on :${PORT}, scratch dir $OUT"
STREAMABLE=1 PORT="${PORT}" "${REPO_ROOT}/bin/testserver" > "$OUT/server.log" 2>&1 &
PID=$!
trap 'kill $PID 2>/dev/null; wait $PID 2>/dev/null' EXIT

for i in $(seq 1 30); do
    curl -sf -o /dev/null -X POST "http://localhost:${PORT}/mcp" \
        -H "Content-Type: application/json" \
        -H "Accept: application/json, text/event-stream" \
        -d '{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"conf-mrtr","version":"0"}}}' \
        2>/dev/null && break
    sleep 1
done

SCENARIOS=$(cd "${MCPCONFORMANCE_MRTR_PATH}" && node dist/index.js list --server 2>/dev/null \
    | awk '/^  - /{print $2}' | grep '^input-required-result-')
if [ -z "$SCENARIOS" ]; then
    echo "testconf-mrtr: upstream listed no input-required-result-* scenarios"
    exit 1
fi

for scenario in $SCENARIOS; do
    (cd "${MCPCONFORMANCE_MRTR_PATH}" && \
        node dist/index.js server \
            --url "http://localhost:${PORT}/mcp" \
            --scenario "$scenario" \
            --suite all \
            -o "$OUT/checks" >> "$OUT/runner.log" 2>&1)
done

CHECKS=$(find "$OUT/checks" -name checks.json 2>/dev/null)
if [ -z "$CHECKS" ]; then
    echo "testconf-mrtr: upstream runner produced no checks.json"
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
echo "testconf-mrtr: $PASSES pass / $FAILS fail / $WARNS warn / $SKIPS skip across $NSCEN scenarios (artifacts: $OUT/checks)"

if [ "$FAILS" -gt 0 ]; then
    echo "Unexpected FAILURE rows:"
    # shellcheck disable=SC2086
    grep -B 4 '"status": "FAILURE"' $CHECKS | head -80
    exit 1
fi

kill $PID 2>/dev/null; wait $PID 2>/dev/null; PID=""
trap - EXIT

# Upstream negative suite — broken fixture, spawned by the test file itself.
(cd "$MCPCONFORMANCE_MRTR_PATH" && npx vitest run src/scenarios/server/negative-mrtr.test.ts) || exit 1
# mcpkit-stricter sentinel.
(cd "$CONFORMANCE_DIR" && npm install --silent && npx vitest run mrtr/) || exit 1
