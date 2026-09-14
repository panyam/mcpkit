#!/usr/bin/env bash
# SEP-2640 skills conformance — drives examples/skills through the three
# upstream `sep-2640-skills-*` server scenarios. Requires zero FAILURE rows.
#
# Until PR 330 merged this pointed at the panyam/mcpconformance fork branch
# chore/sep-2640-yaml and was INFO-only, exiting 0 regardless of results while
# the WG iterated sep-2640.yaml. The scenarios are now on upstream main
# (7169291) under the same names, so the retarget was a path swap and the
# stage gates like every other conformance suite.
#
# Runner-agnostic: the Makefile + justfile `testconf-skills` recipes call this
# directly. REPO_ROOT + MCPCONFORMANCE_SKILLS_PATH resolve via _common.sh
# (path-defaults.sh); override the path via env.
set -u
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_common.sh"

require_conf_dir MCPCONFORMANCE_SKILLS_PATH \
    "Clone https://github.com/modelcontextprotocol/conformance there:" \
    "  git clone https://github.com/modelcontextprotocol/conformance.git ${MCPCONFORMANCE_SKILLS_PATH}" \
    "Or set MCPCONFORMANCE_SKILLS_PATH=<path-to-clone>."

PORT="${SKILLS_PORT:-18099}"

build_conf_dist MCPCONFORMANCE_SKILLS_PATH

(cd "${REPO_ROOT}/examples/skills" && go build -o skills-demo .) || exit 1

OUT=$(mktemp -d -t conf-skills.XXXXXX)
echo "Spawning examples/skills on :${PORT}, scratch dir $OUT"
"${REPO_ROOT}/examples/skills/skills-demo" --serve --addr=":${PORT}" \
    --skills="${REPO_ROOT}/examples/skills/skills" > "$OUT/server.log" 2>&1 &
PID=$!
trap 'kill $PID 2>/dev/null; wait $PID 2>/dev/null' EXIT

for i in $(seq 1 30); do
    curl -sf -o /dev/null -X OPTIONS "http://localhost:${PORT}/mcp" && break
    sleep 0.3
done

# The SEP-2640 server surface is split across three scenarios: enumeration,
# SKILL.md manifest, and resources/directory/read. Run by exact name because
# upstream registers them in the *pending* suite — the everything-server does
# not implement io.modelcontextprotocol/skills, so a suite-wide run skips them.
#
# `sep-2640-skills-index` was renamed to `sep-2640-skills-enumeration` when the
# 2026-08-21 SEP revision retired skill://index.json in favour of skills/list.
#
# Upstream also registers five client-side scenarios (sep-2640-client-no-prefetch
# and sep-2640-client-verify-{digest,size,frontmatter}). They need a client
# driver rather than this fixture and are not run here.
SKILLS_SCENARIOS="sep-2640-skills-enumeration sep-2640-skills-manifest sep-2640-skills-directory"

for scenario in ${SKILLS_SCENARIOS}; do
    (cd "${MCPCONFORMANCE_SKILLS_PATH}" && \
        node dist/index.js server \
            --url "http://localhost:${PORT}/mcp" \
            --scenario "$scenario" \
            -o "$OUT/checks" >> "$OUT/runner.log" 2>&1)
done

CHECKS=$(find "$OUT/checks" -name checks.json 2>/dev/null)
if [ -z "$CHECKS" ]; then
    echo "testconf-skills: upstream runner produced no checks.json"
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
NSCEN=$(echo "$SKILLS_SCENARIOS" | wc -w | tr -d ' ')
echo "testconf-skills: $PASSES pass / $FAILS fail / $WARNS warn / $SKIPS skip across $NSCEN scenarios (artifacts: $OUT/checks)"

if [ "$FAILS" -gt 0 ]; then
    echo "Unexpected FAILURE rows:"
    # shellcheck disable=SC2086
    grep -B 4 '"status": "FAILURE"' $CHECKS | head -80
    exit 1
fi
