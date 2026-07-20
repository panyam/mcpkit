#!/usr/bin/env bash
# SEP-2322 MRTR conformance — upstream scenarios + mcpkit-local stricter
# sentinel.
#
# Runner-agnostic: the Makefile + justfile `testconf-mrtr` recipes call this
# directly. REPO_ROOT + CONFORMANCE_DIR + MCPCONFORMANCE_MRTR_PATH resolve via
# _common.sh (path-defaults.sh); override the path via env.
set -eu
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_common.sh"

require_conf_dir MCPCONFORMANCE_MRTR_PATH \
    "Clone https://github.com/modelcontextprotocol/conformance there or set MCPCONFORMANCE_MRTR_PATH=<path-to-clone>."

(cd "$REPO_ROOT/examples/mrtr" && go build -o mrtr-demo .)
(cd "$MCPCONFORMANCE_MRTR_PATH" && npm install --silent && \
    MRTR_SERVER_URL=http://localhost:18093/mcp \
    MRTR_SERVER_CMD="$REPO_ROOT/examples/mrtr/mrtr-demo --serve --addr :18093" \
    npx vitest run src/scenarios/server/negative-mrtr.test.ts)
(cd "$CONFORMANCE_DIR" && npm install --silent && npx vitest run mrtr/)
