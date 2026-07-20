#!/usr/bin/env bash
# MCP Tasks v1 conformance — builds + starts the server, runs the tsx
# scenarios, tears down.
#
# Runner-agnostic: the Makefile + justfile `testconf-tasks` recipes call this
# directly. REPO_ROOT + CONFORMANCE_DIR resolve via _common.sh.
set -u
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_common.sh"

cd "$REPO_ROOT/examples/tasks" && go build -o tasks .
"$REPO_ROOT/examples/tasks/tasks" --serve -addr :18091 &
PID=$!
for i in $(seq 1 30); do
    curl -sf -o /dev/null -X POST http://localhost:18091/mcp \
        -H "Content-Type: application/json" \
        -H "Accept: application/json, text/event-stream" \
        -d '{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"healthcheck","version":"0.0"}}}' \
        && break
    sleep 0.5
done
(cd "$CONFORMANCE_DIR" && npm install --silent && \
    SERVER_URL=http://localhost:18091/mcp npx tsx --test tasks/scenarios.test.ts)
RC=$?
kill $PID 2>/dev/null
wait $PID 2>/dev/null
exit $RC
