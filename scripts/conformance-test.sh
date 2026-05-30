#!/bin/bash
# Runs the official MCP conformance test suite against the mcpkit test server.
# Requires Node.js/npx for @modelcontextprotocol/conformance.
#
# Usage:
#   bash scripts/conformance-test.sh              # Run full suite
#   bash scripts/conformance-test.sh <scenario>   # Run single scenario
#
# Environment:
#   CONF_PORT     — port for test server (default: 18799)
#   CONF_VERBOSE  — set to 1 for verbose output
set -euo pipefail

PORT="${CONF_PORT:-18799}"
SCENARIO="${1:-}"
SERVER_PID=""
BASELINE="conformance/baseline.yml"

cleanup() {
    if [ -n "$SERVER_PID" ]; then
        kill "$SERVER_PID" 2>/dev/null || true
        wait "$SERVER_PID" 2>/dev/null || true
    fi
}
trap cleanup EXIT

# Check for npx (Node.js 22+ required for @modelcontextprotocol/conformance)
if ! command -v npx &>/dev/null; then
    echo "FAIL: npx not found. Install Node.js 22+ to run conformance tests."
    exit 1
fi

# Kill any stale test server on the port to avoid connecting to old code
if lsof -i ":$PORT" -t >/dev/null 2>&1; then
    echo "Killing stale process on port $PORT..."
    lsof -i ":$PORT" -t | xargs kill 2>/dev/null || true
    sleep 1
fi

echo "=== Building test server ==="
# Build first, then run the binary directly. Previously this script used
# `go run ./cmd/testserver &` and captured $! — but on macOS `go run` spawns
# the compiled binary as a separate child, so killing $SERVER_PID at cleanup
# leaves the child as an orphan that hangs `make testconf` after a
# successful run (the npm conformance harness exits clean, but make waits
# on the lingering testserver). Building to a binary makes $! unambiguously
# the fixture PID, matching the pattern testconf-tasks already uses.
go build -o cmd/testserver/testserver ./cmd/testserver

echo "=== Starting test server on :$PORT (Streamable HTTP) ==="
STREAMABLE=1 PORT=$PORT ./cmd/testserver/testserver &
SERVER_PID=$!

# Wait for server to be ready
echo -n "Waiting for server..."
for i in $(seq 1 30); do
    if curl -s -o /dev/null -w "%{http_code}" -X POST "http://localhost:$PORT/mcp" \
        -H "Content-Type: application/json" \
        -H "Accept: application/json, text/event-stream" \
        -d '{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"healthcheck","version":"0.0"}}}' 2>/dev/null | grep -q "200"; then
        echo " ready"
        break
    fi
    echo -n "."
    sleep 1
done

if ! kill -0 "$SERVER_PID" 2>/dev/null; then
    echo " FAIL: server exited"
    exit 1
fi

# Build conformance command
CONF_CMD="npx -y @modelcontextprotocol/conformance server --url http://localhost:$PORT/mcp"

if [ -f "$BASELINE" ]; then
    CONF_CMD="$CONF_CMD --expected-failures $BASELINE"
fi

if [ -n "$SCENARIO" ]; then
    CONF_CMD="$CONF_CMD --scenario $SCENARIO"
fi

if [ "${CONF_VERBOSE:-}" = "1" ]; then
    CONF_CMD="$CONF_CMD --verbose"
fi

echo ""
echo "=== Running MCP conformance suite ==="
echo "Command: $CONF_CMD"
echo ""

# Run conformance suite — capture exit code
set +e
eval "$CONF_CMD"
EXIT_CODE=$?
set -e

echo ""
if [ "$EXIT_CODE" -eq 0 ]; then
    echo "=== CONFORMANCE TESTS PASSED ==="
else
    echo "=== CONFORMANCE TESTS FAILED (exit code $EXIT_CODE) ==="
    echo ""
    echo "If these are expected failures, add them to $BASELINE"
    echo "See: https://github.com/modelcontextprotocol/conformance"
fi

exit "$EXIT_CODE"
