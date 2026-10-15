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
    conf_source_status "$var_name"
}

# conf_source_status VAR_NAME
# Print the checked-out commit of the suite worktree named by $VAR_NAME, and
# how far behind its tracked upstream branch it is.
#
# Deliberately a warning, not a gate, and deliberately not an automatic pull.
# Pinning the checkout is what keeps a run reproducible: auto-pulling would
# make `make testconf-*` start failing mid-unrelated-work because upstream
# landed a scenario, would mutate a worktree four suites share
# (../conf-upstream-main backs tasks-v2, mrtr, client and stateless), and would
# fight the CONFORMANCE.md staleness gate, which pins
# `upstream-conformance@<sha>`.
#
# What was actually missing is visibility. Nothing surfaced that ../conf-pending
# had drifted 45 commits behind, or that ../conf-upstream-main predated the
# conformance PR 468 merge. Now every run says so.
#
# Set CONF_PULL=1 to fast-forward before running. Offline degrades to just the
# SHA line: the fetch is best-effort and never fails the suite.
conf_source_status() {
    local var_name="$1"
    local dir="${!var_name}"
    local sha branch behind
    sha=$(git -C "$dir" rev-parse --short HEAD 2>/dev/null) || return 0
    branch=$(git -C "$dir" rev-parse --abbrev-ref HEAD 2>/dev/null)
    echo "${var_name}: ${dir}"
    echo "  checked out ${branch} @ ${sha}"

    if [ "${CONF_PULL:-}" = "1" ]; then
        echo "  CONF_PULL=1, fast-forwarding..."
        git -C "$dir" pull --ff-only 2>&1 | sed 's/^/    /'
        sha=$(git -C "$dir" rev-parse --short HEAD 2>/dev/null)
        echo "  now at ${sha}"
        return 0
    fi

    # Best-effort freshness check. A detached HEAD or an unreachable origin
    # (offline, or a PR-ref worktree like ../conf-481) just skips it.
    if ! git -C "$dir" fetch --quiet 2>/dev/null; then
        echo "  (could not reach origin; skipping freshness check)"
        return 0
    fi
    behind=$(git -C "$dir" rev-list --count "HEAD..@{u}" 2>/dev/null) || return 0
    if [ -n "$behind" ] && [ "$behind" -gt 0 ]; then
        echo "  WARNING: ${behind} commit(s) behind @{u}. Grading against an older suite."
        echo "           Re-run with CONF_PULL=1, or: git -C ${dir} pull --ff-only"
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

# require_port_free PORT
# Refuse to start a fixture on a port something else already holds.
#
# Every fixture here is spawned in the background and the readiness loop only
# waits for *something* to answer. If the port is taken, the fresh fixture
# dies on "bind: address already in use" and the suite grades whatever is
# listening instead, reporting plausible numbers for code that was never under
# test (#1513: testconf-events graded a leftover kitchen-sink and showed
# discovery, poll and push green). Probes with bash's /dev/tcp, so it needs no
# lsof or nc on the runner; lsof only adds the holder's PID when present.
require_port_free() {
    local port="$1"
    if (exec 3<>"/dev/tcp/127.0.0.1/${port}") 2>/dev/null; then
        local holder=""
        if command -v lsof >/dev/null 2>&1; then
            holder=$(lsof -t -nP -iTCP:"${port}" -sTCP:LISTEN 2>/dev/null | head -1)
        fi
        echo "REFUSING: port ${port} is already in use${holder:+ by PID ${holder}}." >&2
        echo "A fixture started now would fail to bind, and the suite would grade whatever holds the port." >&2
        echo "Stop it (or pick another port) and re-run." >&2
        exit 1
    fi
}

# first_free_port START
# Print the first port from START up (at most 50 tried) that nothing answers on.
#
# For a fixture whose port nobody asked for: several sessions on one machine
# now run the same suite, and refusing a busy default makes the second wait
# for the first (#1534). A port someone set explicitly still goes through
# require_port_free instead, so an explicit choice is never silently moved.
# The probe-then-bind gap is a race in principle; require_fixture_alive
# catches the fixture that lost it.
first_free_port() {
    local port="$1" last=$(( $1 + 50 ))
    while [ "$port" -le "$last" ]; do
        if ! (exec 3<>"/dev/tcp/127.0.0.1/${port}") 2>/dev/null; then
            echo "$port"
            return 0
        fi
        port=$(( port + 1 ))
    done
    echo "No free port in ${1}-${last}." >&2
    exit 1
}

# require_fixture_alive PID [LOG]
# Refuse to grade if the fixture this script spawned has already exited.
#
# Run after the readiness loop. The loop breaks on the first answer from the
# port, which says nothing about whose answer it was, and it falls through
# silently when nothing answers at all. A dead PID here means the suite would
# grade a stranger or nothing. LOG, when given, is the fixture's own output,
# and its tail usually names the reason.
require_fixture_alive() {
    local pid="$1" log="${2:-}"
    if ! kill -0 "${pid}" 2>/dev/null; then
        echo "REFUSING: the fixture (PID ${pid}) exited before grading started." >&2
        if [ -n "${log}" ] && [ -f "${log}" ]; then
            echo "Last lines of ${log}:" >&2
            tail -5 "${log}" >&2
        fi
        exit 1
    fi
}
