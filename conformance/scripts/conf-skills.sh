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
(cd "${MCPCONFORMANCE_SKILLS_PATH}" && \
    node dist/index.js server \
        --url http://localhost:18099/mcp \
        --scenario sep-2640-skills \
        -o "$OUT/checks" > "$OUT/runner.log" 2>&1)
RC=$?
kill $PID 2>/dev/null
wait $PID 2>/dev/null
if [ $RC -ne 0 ]; then
    echo "==================================================================="
    echo "testconf-skills: INFORMATIONAL — runner exited $RC (artifacts in $OUT)"
    echo "This suite is INFO status in conformance/local-suites.yaml while the"
    echo "fork-side Scenario classes iterate on sep-2640.yaml (mcpconformance"
    echo "PR 330). The check failures are pre-existing input-required-result"
    echo "(SEP-2567) gaps the skills runner also exercises, not new regressions."
    echo "Exiting 0 so the umbrella reaches refresh-conformance. See issue 613."
    echo "==================================================================="
    tail -30 "$OUT/runner.log" 2>/dev/null
fi
