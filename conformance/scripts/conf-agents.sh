#!/usr/bin/env bash
# Agent discovery conformance (agents-wg#22), informational (exit 0 regardless
# of check results). Drives examples/agents/discovery via the fork's scenario
# runner.
#
# INFO rather than a gate because agents-wg#22 is still a draft in the Agents
# WG repository and has no SEP number yet, so its check IDs carry the sep-9998
# placeholder (see the header of src/seps/sep-9998.yaml on the fork branch).
# Flip this to a gate once the SEP is accepted and numbered.
#
# Runner-agnostic: the Makefile + justfile `testconf-agents` recipes call this
# directly. REPO_ROOT + MCPCONFORMANCE_AGENTS_PATH resolve via _common.sh
# (path-defaults.sh); override the latter via env.
set -u
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_common.sh"

require_conf_dir MCPCONFORMANCE_AGENTS_PATH \
    "Run 'make conf-agents-setup' to create it, or set MCPCONFORMANCE_AGENTS_PATH=<path-to-clone>." \
    "Default expects the feat/agents-conformance-suite branch checked out at ../conf-agents."
build_conf_dist MCPCONFORMANCE_AGENTS_PATH

BIN_DIR=$(mktemp -d -t conf-agents-bin.XXXXXX)
(cd "${REPO_ROOT}/examples/agents/discovery" && go build -o "${BIN_DIR}/agents-discovery" .)

OUT=$(mktemp -d -t conf-agents.XXXXXX)
echo "Spawning fixture on :18102, scratch dir $OUT"

# --conformance-agents serves anonymously, pages one agent at a time, adds an
# agent naming a missing tool, and registers the two agents_conformance_*
# controls the scenarios drive. Without it the hidden-agent, broken-agent and
# list_changed rows report untestable. Off by default because the example is a
# published walkthrough first; see examples/CONVENTIONS.md § Conformance
# fixtures.
require_port_free 18102
"${BIN_DIR}/agents-discovery" --serve -addr=:18102 --conformance-agents > "$OUT/server.log" 2>&1 &
PID=$!
for i in 1 2 3 4 5 6 7 8 9 10; do
    curl -sf -o /dev/null -X OPTIONS http://localhost:18102/mcp && break
    sleep 0.3
done
require_fixture_alive "$PID" "$OUT/server.log"

# --force because agents-wg#22 is not on the spec timeline, so the runner will
# not select these scenarios for any spec version on its own.
AGENTS_SCENARIOS="sep-9998-agents-discovery sep-9998-agents-list-changed"
RC=0
for S in ${AGENTS_SCENARIOS}; do
    (cd "${MCPCONFORMANCE_AGENTS_PATH}" && \
        node dist/index.js server \
            --url http://localhost:18102/mcp \
            --scenario "${S}" \
            --spec-version 2026-07-28 --force \
            -o "$OUT/checks-${S}" > "$OUT/runner-${S}.log" 2>&1)
    SRC=$?
    SUMMARY=$(grep -E "Passed:" "$OUT/runner-${S}.log" | tail -1 | sed 's/\x1b\[[0-9;]*m//g')
    echo "  ${S}: ${SUMMARY:-runner exited ${SRC} (see $OUT/runner-${S}.log)}"
    [ ${SRC} -ne 0 ] && RC=${SRC}
done

kill $PID 2>/dev/null
wait $PID 2>/dev/null
rm -rf "${BIN_DIR}"

if [ $RC -ne 0 ]; then
    echo "==================================================================="
    echo "testconf-agents: INFORMATIONAL, a runner invocation exited $RC (artifacts in $OUT)"
    echo "This suite is INFO status in conformance/local-suites.yaml."
    echo "Exiting 0 so the umbrella reaches refresh-conformance."
    echo "==================================================================="
    for S in ${AGENTS_SCENARIOS}; do
        echo "--- ${S} ---"; tail -20 "$OUT/runner-${S}.log" 2>/dev/null
    done
fi
exit 0
