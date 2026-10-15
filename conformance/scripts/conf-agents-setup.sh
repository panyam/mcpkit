#!/usr/bin/env bash
# Create the checkout the agents suite reads, if it is missing. Safe to
# re-run: an existing checkout is reported and left alone, never pulled, so a
# run stays pinned to whatever is checked out (see conf_source_status in
# _common.sh for why nothing here auto-pulls).
#
#   MCPCONFORMANCE_AGENTS_PATH  server scenarios, feat/agents-conformance-suite
#
# The branch name mirrors conformance/local-suites.yaml.
set -eu
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_common.sh"

FORK_URL="https://github.com/panyam/mcpconformance.git"
BRANCH="feat/agents-conformance-suite"

if [ -d "${MCPCONFORMANCE_AGENTS_PATH}" ]; then
    echo "checkout exists: ${MCPCONFORMANCE_AGENTS_PATH}"
else
    echo "cloning ${FORK_URL} (${BRANCH}) into ${MCPCONFORMANCE_AGENTS_PATH}"
    git clone --branch "${BRANCH}" "${FORK_URL}" "${MCPCONFORMANCE_AGENTS_PATH}"
fi

echo "done. The suite builds its own dist/ on every run, so there is no install step here."
