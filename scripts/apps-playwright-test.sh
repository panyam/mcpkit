#!/usr/bin/env bash
# Run the ext-apps Playwright test suite against mcpkit's test server.
#
# Prerequisites:
#   - Node.js 22+ with npx
#   - Playwright browsers installed (npx playwright install)
#
# Usage:
#   make test-apps-playwright
#   # or directly:
#   bash scripts/apps-playwright-test.sh
#
# Environment:
#   PORT          Test server port (default: 18799)
#   VERBOSE       Set to 1 for verbose output
#   EXT_APPS_DIR  Path to ext-apps checkout (default: /tmp/ext-apps)

set -euo pipefail

PORT="${PORT:-18799}"
EXT_APPS_DIR="${EXT_APPS_DIR:-/tmp/ext-apps}"
EXT_APPS_REPO="https://github.com/modelcontextprotocol/ext-apps.git"
SERVER_PID=""

cleanup() {
    if [ -n "$SERVER_PID" ]; then
        kill "$SERVER_PID" 2>/dev/null || true
        wait "$SERVER_PID" 2>/dev/null || true
    fi
}
trap cleanup EXIT

# Check prerequisites
if ! command -v npx &>/dev/null; then
    echo "ERROR: npx not found. Install Node.js 22+."
    exit 1
fi

# Clone or update ext-apps
if [ -d "$EXT_APPS_DIR/.git" ]; then
    echo "Updating ext-apps in $EXT_APPS_DIR..."
    (cd "$EXT_APPS_DIR" && git pull --quiet)
else
    echo "Cloning ext-apps to $EXT_APPS_DIR..."
    git clone --quiet "$EXT_APPS_REPO" "$EXT_APPS_DIR"
fi

# Install Playwright deps if needed
(cd "$EXT_APPS_DIR" && npm install --silent 2>/dev/null && npx playwright install --with-deps chromium 2>/dev/null) || {
    echo "WARNING: Playwright setup failed. Run manually:"
    echo "  cd $EXT_APPS_DIR && npm install && npx playwright install"
    exit 1
}

# Kill stale process on port
if lsof -ti:$PORT >/dev/null 2>&1; then
    echo "Killing stale process on port $PORT..."
    lsof -ti:$PORT | xargs kill -9 2>/dev/null || true
    sleep 1
fi

# Start mcpkit test server
echo "Starting mcpkit test server on port $PORT..."
STREAMABLE=1 PORT=$PORT go run ./cmd/testserver &
SERVER_PID=$!

# Wait for server readiness
echo "Waiting for server..."
for i in $(seq 1 30); do
    if curl -sf -X POST "http://localhost:$PORT/mcp" \
        -H "Content-Type: application/json" \
        -H "Accept: application/json, text/event-stream" \
        -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"probe","version":"0.0.0"}}}' \
        -o /dev/null 2>/dev/null; then
        echo "Server ready."
        break
    fi
    if [ "$i" -eq 30 ]; then
        echo "ERROR: Server failed to start within 30 seconds."
        exit 1
    fi
    sleep 1
done

# Run Playwright tests
echo ""
echo "=== Running ext-apps Playwright tests ==="
echo "Server: http://localhost:$PORT/mcp"
echo ""

PLAYWRIGHT_ARGS=""
if [ "${VERBOSE:-}" = "1" ]; then
    PLAYWRIGHT_ARGS="--reporter=verbose"
fi

cd "$EXT_APPS_DIR"
MCP_SERVER_URL="http://localhost:$PORT/mcp" npx playwright test $PLAYWRIGHT_ARGS
EXIT_CODE=$?

if [ $EXIT_CODE -eq 0 ]; then
    echo ""
    echo "=== Playwright tests PASSED ==="
else
    echo ""
    echo "=== Playwright tests FAILED (exit $EXIT_CODE) ==="
fi

exit $EXIT_CODE
