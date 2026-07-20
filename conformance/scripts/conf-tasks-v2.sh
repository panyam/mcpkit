#!/usr/bin/env bash
# SEP-2663 tasks conformance — upstream scenarios + mcpkit-local stricter
# sentinel.
#
# Runner-agnostic: the Makefile + justfile `testconf-tasks-v2` recipes call
# this directly. REPO_ROOT + CONFORMANCE_DIR + MCPCONFORMANCE_TASKS_V2_PATH
# resolve via _common.sh (path-defaults.sh); override the path via env.
set -eu
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_common.sh"

require_conf_dir MCPCONFORMANCE_TASKS_V2_PATH \
    "Clone https://github.com/modelcontextprotocol/conformance there or set MCPCONFORMANCE_TASKS_V2_PATH=<path-to-clone>."

(cd "$REPO_ROOT/examples/tasks-v2" && go build -o tasks-v2 .)
(cd "$MCPCONFORMANCE_TASKS_V2_PATH" && npm install --silent && \
    TASKS_SERVER_URL=http://localhost:18092/mcp \
    TASKS_SERVER_CMD="$REPO_ROOT/examples/tasks-v2/tasks-v2 --serve --addr :18092" \
    npx vitest run src/scenarios/server/all-scenarios.test.ts)
(cd "$CONFORMANCE_DIR" && npm install --silent && npx vitest run tasks-v2/)
