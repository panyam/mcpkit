#!/usr/bin/env bash
# Runs a command in each sub-module that has a go.mod, reporting per module.
#
# The module list arrives in MODS, from the root Makefile. That is deliberate:
# CLAUDE.md makes SUB_MODS_TO_TAG in the Makefile the authoritative list, so
# this script must not grow a second copy of it.
#
# Usage: submodule-loop.sh <label> <command...>
#   MODS       space-separated module paths (required)
#   KEEP_GOING when set, run every module and fail at the end with a summary,
#              instead of stopping at the first failure
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

LABEL="${1:?usage: submodule-loop.sh <label> <command...>}"
shift
MODS="${MODS:-}"
KEEP_GOING="${KEEP_GOING:-}"

failed=""
for mod in $MODS; do
    [ -f "$mod/go.mod" ] || continue
    echo ""
    echo "==> $LABEL $mod"
    if ! (cd "$mod" && "$@"); then
        failed="$failed $mod"
        [ -n "$KEEP_GOING" ] || { echo "=== $LABEL FAILED in:$failed ==="; exit 1; }
    fi
done

if [ -n "$failed" ]; then
    echo ""
    echo "=== $LABEL FAILED in:$failed ==="
    exit 1
fi
