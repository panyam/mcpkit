#!/usr/bin/env bash
# SEP-2640 skills conformance — informational (exit 0 regardless of check
# results while the WG iterates sep-2640.yaml). Drives examples/skills via the
# fork's scenario runner.
#
# Runner-agnostic: the Makefile + justfile `testconf-skills` recipes call this
# directly. REPO_ROOT + MCPCONFORMANCE_SKILLS_PATH resolve via _common.sh
# (path-defaults.sh); override the latter via env.
set -u
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_common.sh"

require_conf_dir MCPCONFORMANCE_SKILLS_PATH \
    "Clone https://github.com/panyam/mcpconformance there or set MCPCONFORMANCE_SKILLS_PATH=<path-to-clone>." \
    "Default expects the chore/sep-2640-yaml branch checked out at ../conf-skills."
(cd "${REPO_ROOT}/examples/skills" && go build -o skills-demo .)
OUT=$(mktemp -d -t conf-skills.XXXXXX)
echo "Spawning fixture on :18099, scratch dir $OUT"
"${REPO_ROOT}/examples/skills/skills-demo" --serve --addr=:18099 --skills="${REPO_ROOT}/examples/skills/skills" > "$OUT/server.log" 2>&1 &
PID=$!
for i in 1 2 3 4 5 6 7 8 9 10; do
    curl -sf -o /dev/null -X OPTIONS http://localhost:18099/mcp && break
    sleep 0.3
done
# The SEP-2640 server surface is split across three scenarios (mcpconformance
# PR 330): index shape, SKILL.md manifest, and resources/directory/read. Each is
# registered in both the active and pending suites and run by exact name.
SKILLS_SCENARIOS="sep-2640-skills-index sep-2640-skills-manifest sep-2640-skills-directory"
RC=0
for S in ${SKILLS_SCENARIOS}; do
    (cd "${MCPCONFORMANCE_SKILLS_PATH}" && \
        node dist/index.js server \
            --url http://localhost:18099/mcp \
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
    echo "testconf-skills: INFORMATIONAL — a runner invocation exited $RC (artifacts in $OUT)"
    echo "This suite is INFO status in conformance/local-suites.yaml while the"
    echo "fork-side Scenario classes iterate on sep-2640.yaml (mcpconformance"
    echo "PR 330). Exiting 0 so the umbrella reaches refresh-conformance. See issue 613."
    echo "==================================================================="
    for S in ${SKILLS_SCENARIOS}; do
        echo "--- ${S} ---"; tail -20 "$OUT/runner-${S}.log" 2>/dev/null
    done
fi
exit 0
