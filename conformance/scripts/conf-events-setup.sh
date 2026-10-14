#!/usr/bin/env bash
# Create the two checkouts the events suites read, if they are missing.
# Safe to re-run: an existing checkout is reported and left alone, never
# pulled, so a run stays pinned to whatever is checked out (see
# conf_source_status in _common.sh for why nothing here auto-pulls).
#
#   MCPCONFORMANCE_EVENTS_PATH         server scenarios, feat/events-conformance-suite
#   MCPCONFORMANCE_EVENTS_CLIENT_PATH  client scenarios, feat/events-client-conformance
#
# The client checkout is a worktree of the server one, so the two share one
# clone and one fetch. The branch names mirror conformance/local-suites.yaml.
set -eu
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_common.sh"

FORK_URL="https://github.com/panyam/mcpconformance.git"
SERVER_BRANCH="feat/events-conformance-suite"
CLIENT_BRANCH="feat/events-client-conformance"

if [ -d "${MCPCONFORMANCE_EVENTS_PATH}" ]; then
    echo "server checkout exists: ${MCPCONFORMANCE_EVENTS_PATH}"
else
    echo "cloning ${FORK_URL} (${SERVER_BRANCH}) into ${MCPCONFORMANCE_EVENTS_PATH}"
    git clone --branch "${SERVER_BRANCH}" "${FORK_URL}" "${MCPCONFORMANCE_EVENTS_PATH}"
fi

if [ -d "${MCPCONFORMANCE_EVENTS_CLIENT_PATH}" ]; then
    echo "client checkout exists: ${MCPCONFORMANCE_EVENTS_CLIENT_PATH}"
else
    echo "adding worktree ${MCPCONFORMANCE_EVENTS_CLIENT_PATH} (${CLIENT_BRANCH})"
    git -C "${MCPCONFORMANCE_EVENTS_PATH}" fetch --quiet origin "${CLIENT_BRANCH}"
    if git -C "${MCPCONFORMANCE_EVENTS_PATH}" show-ref --verify --quiet "refs/heads/${CLIENT_BRANCH}"; then
        git -C "${MCPCONFORMANCE_EVENTS_PATH}" worktree add "${MCPCONFORMANCE_EVENTS_CLIENT_PATH}" "${CLIENT_BRANCH}"
    else
        git -C "${MCPCONFORMANCE_EVENTS_PATH}" worktree add --track -b "${CLIENT_BRANCH}" \
            "${MCPCONFORMANCE_EVENTS_CLIENT_PATH}" "origin/${CLIENT_BRANCH}"
    fi
fi

echo "done. Each suite builds its own dist/ on every run, so there is no install step here."
