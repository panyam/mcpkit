#!/usr/bin/env bash
# SEP-2575 stateless conformance — drives examples/stateless via upstream's
# ServerStatelessScenario class. Requires zero FAILURE rows.
#
# Runner-agnostic: the Makefile + justfile `testconf-stateless` recipes call
# this directly. REPO_ROOT + MCPCONFORMANCE_STATELESS_PATH resolve via
# _common.sh (path-defaults.sh); override the latter via env.
set -u
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_common.sh"

require_conf_dir MCPCONFORMANCE_STATELESS_PATH \
    "Clone https://github.com/modelcontextprotocol/conformance there:" \
    "  git clone https://github.com/modelcontextprotocol/conformance.git ${MCPCONFORMANCE_STATELESS_PATH}" \
    "Or set MCPCONFORMANCE_STATELESS_PATH=<path-to-clone>."
(cd "${REPO_ROOT}/examples/stateless" && go build -o stateless-demo .)
OUT=$(mktemp -d -t conf-stateless.XXXXXX)
echo "Spawning fixture on :18100, scratch dir $OUT"
"${REPO_ROOT}/examples/stateless/stateless-demo" --serve --addr=:18100 > "$OUT/server.log" 2>&1 &
PID=$!
for i in 1 2 3 4 5 6 7 8 9 10; do
    curl -sf -X POST -H "Content-Type: application/json" \
        -d '{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"poll","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}' \
        -H "MCP-Protocol-Version: 2026-07-28" \
        http://localhost:18100/mcp > /dev/null 2>&1 && break
    sleep 0.3
done
(cd "${MCPCONFORMANCE_STATELESS_PATH}" && \
    node dist/index.js server \
        --url http://localhost:18100/mcp \
        --scenario server-stateless \
        -o "$OUT/checks" > "$OUT/runner.log" 2>&1)
RC=$?
kill $PID 2>/dev/null
wait $PID 2>/dev/null
CHECKS=$(ls -t "$OUT"/checks/server-server-stateless-*/checks.json 2>/dev/null | head -1)
if [ -z "$CHECKS" ] || [ ! -f "$CHECKS" ]; then
    echo "testconf-stateless: upstream runner produced no checks.json"
    tail -30 "$OUT/runner.log"
    exit 1
fi
FAILS=$(grep -c '"status": "FAILURE"' "$CHECKS")
PASSES=$(grep -c '"status": "SUCCESS"' "$CHECKS")
SKIPS=$(grep -c '"status": "SKIPPED"' "$CHECKS")
WARNS=$(grep -c '"status": "WARNING"' "$CHECKS")
echo "testconf-stateless: $PASSES pass / $FAILS fail / $WARNS warn / $SKIPS skip (artifact: $CHECKS)"
if [ "$FAILS" -gt 0 ]; then
    echo "Unexpected FAILURE rows:"
    grep -B 2 '"status": "FAILURE"' "$CHECKS" | head -60
    exit 1
fi
