#!/usr/bin/env bash
# run.sh — build the frontend bundle and serve the agentweb surface over an
# offline streaming demo provider (no model or MCP server needed). This is the
# runnable proof for issue 1197: open the printed URL and drive a turn.
#
#   ./run.sh            # build + serve on :8090
#   ADDR=:9099 ./run.sh # serve on a different address
#
# Requires Node (for the esbuild bundle) and Go. The built bundle lands in
# static/, which the Go binary embeds, so a rebuild is only needed after a
# frontend change.
set -euo pipefail
cd "$(dirname "$0")"

ADDR="${ADDR:-:8090}"

echo "==> building frontend bundle (esbuild)"
( cd web && npm install --silent && npm run build )

echo "==> serving agentweb --demo at http://localhost${ADDR}/"
exec go run ./cmd/agentweb --demo --addr "$ADDR"
