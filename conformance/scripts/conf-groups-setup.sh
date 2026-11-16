#!/usr/bin/env bash
# Create the checkout the groups suite reads, if it is missing. Safe to
# re-run: an existing checkout is reported and left alone, never pulled, so a
# run stays pinned to whatever is checked out (see conf_source_status in
# _common.sh for why nothing here auto-pulls).
#
#   MCPCONFORMANCE_GROUPS_PATH  the groups server scenario, feat/groups-conformance
#
# The branch name mirrors conformance/local-suites.yaml.
set -eu
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_common.sh"

FORK_URL="https://github.com/panyam/mcpconformance.git"
BRANCH="feat/groups-conformance"

if [ -d "${MCPCONFORMANCE_GROUPS_PATH}" ]; then
    echo "checkout exists: ${MCPCONFORMANCE_GROUPS_PATH}"
else
    echo "cloning ${FORK_URL} (${BRANCH}) into ${MCPCONFORMANCE_GROUPS_PATH}"
    git clone --branch "${BRANCH}" "${FORK_URL}" "${MCPCONFORMANCE_GROUPS_PATH}"
fi

echo "done. The suite builds its own dist/ on every run, so there is no install step here."
