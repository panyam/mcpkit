#!/usr/bin/env bash
# SEP-2350 server-side scope challenges — drives
# examples/auth/conformance-scope-challenge against the upstream scenario.
#
# Currently tracks modelcontextprotocol/conformance PR 481, which is unmerged.
# The scenario asserts a 403 + RFC 6750 challenge for an under-scoped token and
# a successful retry with an upgraded one, across tools/call, static and
# templated resources/read, and prompts/get.
#
# Fixture contract (upstream SDK_INTEGRATION.md): two fixed opaque tokens, and
# each operation requires a method-level plus an object-level scope so the
# single-complete-challenge check has something to assert. The SUT implements
# that contract; no authorization server is involved.
#
# Runner-agnostic: the Makefile + justfile `testconf-scope-challenge` recipes
# call this directly. REPO_ROOT + MCPCONFORMANCE_SCOPE_CHALLENGE_PATH resolve
# via _common.sh (path-defaults.sh); override the latter via env.
set -u
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_common.sh"

require_conf_dir MCPCONFORMANCE_SCOPE_CHALLENGE_PATH \
    "Clone https://github.com/modelcontextprotocol/conformance there and check out PR 481:" \
    "  git clone https://github.com/modelcontextprotocol/conformance.git ${MCPCONFORMANCE_SCOPE_CHALLENGE_PATH}" \
    "  git -C ${MCPCONFORMANCE_SCOPE_CHALLENGE_PATH} fetch origin pull/481/head:pr-481 && git -C ${MCPCONFORMANCE_SCOPE_CHALLENGE_PATH} checkout pr-481" \
    "Or set MCPCONFORMANCE_SCOPE_CHALLENGE_PATH=<path-to-clone>."
build_conf_dist MCPCONFORMANCE_SCOPE_CHALLENGE_PATH

PORT="${CONF_SCOPE_CHALLENGE_PORT:-18140}"
(cd "${REPO_ROOT}/examples/auth" && go build -o scope-challenge-sut ./conformance-scope-challenge)
OUT=$(mktemp -d -t conf-scope-challenge.XXXXXX)
echo "Spawning SUT on :${PORT}, scratch dir $OUT"
"${REPO_ROOT}/examples/auth/scope-challenge-sut" -addr ":${PORT}" > "$OUT/server.log" 2>&1 &
PID=$!
for i in 1 2 3 4 5 6 7 8 9 10; do
    curl -sf -o /dev/null "http://localhost:${PORT}/mcp" && break
    sleep 0.3
done

(cd "${MCPCONFORMANCE_SCOPE_CHALLENGE_PATH}" && \
    node dist/index.js server \
        --url "http://localhost:${PORT}/mcp" \
        --scenario sep-2350-server-scope-challenge \
        --spec-version 2026-07-28 \
        -o "$OUT/checks" > "$OUT/runner.log" 2>&1)
RC=$?
kill $PID 2>/dev/null
wait $PID 2>/dev/null
rm -f "${REPO_ROOT}/examples/auth/scope-challenge-sut"

CHECKS=$(ls -t "$OUT"/checks/server-sep-2350-server-scope-challenge-*/checks.json 2>/dev/null | head -1)
if [ -z "$CHECKS" ] || [ ! -f "$CHECKS" ]; then
    echo "testconf-scope-challenge: upstream runner produced no checks.json"
    echo "  (is ${MCPCONFORMANCE_SCOPE_CHALLENGE_PATH} built? run 'npm install && npm run build' there)"
    tail -30 "$OUT/runner.log"
    exit 1
fi
FAILS=$(grep -c '"status": "FAILURE"' "$CHECKS")
PASSES=$(grep -c '"status": "SUCCESS"' "$CHECKS")
SKIPS=$(grep -c '"status": "SKIPPED"' "$CHECKS")
WARNS=$(grep -c '"status": "WARNING"' "$CHECKS")
echo "testconf-scope-challenge: $PASSES pass / $FAILS fail / $WARNS warn / $SKIPS skip (artifact: $CHECKS)"

# Informational while the upstream PR is unmerged: its fixture contract can
# change under us without warning, and a red run then means "upstream moved",
# not "mcpkit regressed". Surfacing the counts is the point. Flip to a hard
# gate once 481 lands on upstream main and the path moves to ../conf-upstream-main.
if [ "$FAILS" -gt 0 ] || [ "$WARNS" -gt 0 ]; then
    echo "Non-SUCCESS rows (informational — upstream PR 481 is unmerged):"
    grep -B 2 -E '"status": "(FAILURE|WARNING)"' "$CHECKS" | head -60
fi
exit 0
