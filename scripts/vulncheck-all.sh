#!/usr/bin/env bash
# govulncheck across the root module and every published sub-module.
#
# Every module runs even after one fails, so a single advisory does not hide
# the rest; the summary at the end names them all.
#
# Note what a clean run does NOT mean: default govulncheck is reachability
# based, so it exits 0 while advisories sit unfixed in required modules.
# Version matching is a separate pass — see DEPENDENCY_POLICY.md.
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

failed=""
echo "==> govulncheck root"
govulncheck ./... || failed="$failed root"

MODS="${SUB_MODS_TO_TAG:-}" KEEP_GOING=1 scripts/submodule-loop.sh govulncheck govulncheck ./... || failed="$failed sub-modules"

echo ""
if [ -n "$failed" ]; then
    echo "=== govulncheck FAILED in:$failed ==="
    exit 1
fi
echo "=== govulncheck clean: root + published sub-modules ==="
