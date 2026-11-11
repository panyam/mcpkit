#!/usr/bin/env bash
# Create the checkouts the file-inputs and auth-server suites read, if they are
# missing (#1551). Safe to re-run: an existing checkout is reported and left
# alone, never pulled, so a run stays pinned to whatever is checked out (see
# conf_source_status in _common.sh for why nothing here auto-pulls).
#
#   MCPCONFORMANCE_FILE_INPUTS_PATH  stage 8f, fork branch `pending`
#   MCPCONFORMANCE_AUTH_PATH         stage 8g, fork branch `pending`
#
# Both default to ../conf-pending, so one clone usually serves both. The branch
# name mirrors conformance/local-suites.yaml.
set -eu
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_common.sh"

FORK_URL="https://github.com/panyam/mcpconformance.git"
BRANCH="pending"

for dir in "${MCPCONFORMANCE_FILE_INPUTS_PATH}" "${MCPCONFORMANCE_AUTH_PATH}"; do
    if [ -d "$dir" ]; then
        echo "checkout exists: $dir"
    else
        echo "cloning ${FORK_URL} (${BRANCH}) into $dir"
        git clone --branch "${BRANCH}" "${FORK_URL}" "$dir"
    fi
done

echo "done. The suites build their own dist/ on every run, so there is no install step here."
