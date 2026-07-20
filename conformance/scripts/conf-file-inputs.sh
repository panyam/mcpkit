#!/usr/bin/env bash
# SEP-2356 file-inputs conformance — fork-based scenarios (auto-spawns fixture).
#
# Runner-agnostic: the Makefile + justfile `testconf-file-inputs` recipes call
# this directly. REPO_ROOT + MCPCONFORMANCE_FILE_INPUTS_PATH resolve via
# _common.sh (path-defaults.sh); override the path via env.
set -eu
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_common.sh"

require_conf_dir MCPCONFORMANCE_FILE_INPUTS_PATH \
    "Clone https://github.com/panyam/mcpconformance there or set MCPCONFORMANCE_FILE_INPUTS_PATH=<path-to-clone>." \
    "Default expects the 'pending' branch checked out at ../conf-pending."

(cd "$REPO_ROOT/examples/file-inputs" && go build -o file-inputs-demo .)
(cd "$MCPCONFORMANCE_FILE_INPUTS_PATH" && npm install --silent && \
    FILE_INPUTS_SERVER_URL=http://localhost:18097/mcp \
    FILE_INPUTS_SERVER_CMD="$REPO_ROOT/examples/file-inputs/file-inputs-demo --serve --addr=:18097" \
    npx vitest run src/scenarios/server/file-inputs/file-inputs.test.ts)
