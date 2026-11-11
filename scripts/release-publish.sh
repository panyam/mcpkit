#!/usr/bin/env bash
# Creates the GitHub Release for a pushed tag, and only once the tag is shown
# to work (#1414).
#
#   release-publish.sh vX.Y.Z     # via `make release-publish V=vX.Y.Z`
#
# It runs three steps in order and stops at the first failure:
#
#   1. check_release_workflows.py --wait: every tag-triggered workflow ran
#      green against V. It polls while one is pending or not registered yet.
#   2. check-install.sh: every command installs from the proxy at V.
#   3. gh release create, with gh's own login (RELEASING.md § GitHub Release).
#
# RELEASING.md used to list steps 1 and 2 as checklist lines before step 3.
# v0.4.0 to v0.6.0 shipped with publish-images.yml never having run, and a
# checklist line is what a tired release skips (#1412).
#
# The notes come from docs/releases/V.md. A stable tag without that file stops
# before any check runs. A -bN pre-release is marked as one and may go without.
#
# RELEASE_WAIT_SECONDS sets how long step 1 polls (default 1200).
# RELEASE_CHECK_WORKFLOWS, RELEASE_CHECK_INSTALL and GH replace the three steps'
# commands. release-publish_selftest.sh uses them to run without the network.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

V="${1:-${V:-}}"
[ -n "$V" ] || { echo "usage: release-publish.sh vX.Y.Z" >&2; exit 2; }
WAIT="${RELEASE_WAIT_SECONDS:-1200}"
PY="${RELEASE_WF_PY:-python3}"
CHECK_WORKFLOWS="${RELEASE_CHECK_WORKFLOWS:-$PY scripts/check_release_workflows.py --wait $WAIT}"
CHECK_INSTALL="${RELEASE_CHECK_INSTALL:-bash scripts/check-install.sh}"
GH="${GH:-gh}"

args=(--repo panyam/mcpkit --title "$V")
prerelease=false
case "$V" in *-b[0-9]*) prerelease=true; args+=(--prerelease) ;; esac
notes="docs/releases/$V.md"
if [ -f "$notes" ]; then
    args+=(--notes-file "$notes")
elif $prerelease; then
    args+=(--notes "Pre-release $V.")
else
    echo "release-publish: $notes does not exist. Write the release note before publishing $V." >&2
    exit 1
fi

echo "==> tag workflows ran green against $V (waits up to ${WAIT}s)"
$CHECK_WORKFLOWS "$V" || {
    echo "release-publish: not publishing $V; a tag workflow did not run green." >&2
    exit 1
}

echo "==> every command installs at $V"
$CHECK_INSTALL "$V" || {
    echo "release-publish: not publishing $V; a command does not install from the proxy." >&2
    echo "  A fresh tag can take minutes to reach proxy.golang.org. Rerun before reading it as broken." >&2
    exit 1
}

echo "==> gh release create $V"
env -u GH_TOKEN $GH release create "$V" "${args[@]}"
