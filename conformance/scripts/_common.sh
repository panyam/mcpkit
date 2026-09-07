#!/usr/bin/env bash
# Shared setup for the runner-agnostic conformance scripts (conf-*.sh).
# Source this at the top of each conf script:
#
#   . "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_common.sh"
#
# It exports CONFORMANCE_DIR + REPO_ROOT and sources path-defaults.sh so
# MCPCONFORMANCE_*_PATH resolve to their sibling-worktree defaults (honoring
# any pre-set env override). It also defines require_conf_dir, the guard every
# fork-based suite uses.

CONFORMANCE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO_ROOT="$(cd "$CONFORMANCE_DIR/.." && pwd)"
export CONFORMANCE_DIR REPO_ROOT

# shellcheck source=/dev/null
. "$CONFORMANCE_DIR/path-defaults.sh"

# require_conf_dir VAR_NAME "hint line 1" ["hint line 2" ...]
# Fail-fast with a remediation message if the worktree named by $VAR_NAME
# (indirect) does not exist.
require_conf_dir() {
    local var_name="$1"; shift
    local dir="${!var_name}"
    if [ ! -d "$dir" ]; then
        echo "${var_name}=${dir} does not exist."
        local line
        for line in "$@"; do
            echo "$line"
        done
        exit 1
    fi
}

# build_conf_dist VAR_NAME
# Build the upstream conformance CLI in the worktree named by $VAR_NAME
# (indirect), so `node dist/index.js` grades against the commit that is
# currently checked out there.
#
# Always rebuilds. A `[ ! -f dist/index.js ]` guard cannot tell a fresh build
# from one left over before a `git pull` in the worktree, and grading against a
# stale validator is worse than not grading at all: it silently reports whatever
# the previous checkout thought. This bit us on conformance issue 424, where a
# dist/ predating the upstream PR 468 merge kept reporting wire-schema failures
# that had already been fixed.
build_conf_dist() {
    local var_name="$1"
    local dir="${!var_name}"
    echo "Building conformance dist/ in ${dir} ..."
    (cd "$dir" && npm install --silent && npm run build >/dev/null) || exit 1
}
