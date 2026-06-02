#!/usr/bin/env bash
# Run upstream's ext-apps Playwright suite against a mcpkit-Go drop-in.
#
# Upstream's tests are written against upstream's own example servers (they
# select by server name in basic-host's dropdown, assert screenshots, etc.).
# To run them against mcpkit, we substitute a mcpkit-Go server that exposes
# the same tool name + resource URI + HTML as upstream's example. basic-host
# (upstream, port 8080) remains the test harness; only the MCP server URL in
# SERVERS env points at our Go fixture instead of upstream's TS server.
#
# Currently scoped to the basic-server-vanillajs example as the PoC. Each
# additional upstream example requires its own mcpkit-Go fixture under
# examples/apps/compat/<name>/.
#
# Prerequisites:
#   - Node.js 22+ with npx
#   - bun (for upstream's basic-host serve script)
#   - Go (for the mcpkit fixture)
#
# Usage:
#   make test-apps-playwright
#   # or directly:
#   bash scripts/apps-playwright-test.sh
#
# Environment:
#   EXT_APPS_DIR    Path to ext-apps checkout (default: /tmp/ext-apps)
#   HARNESS_PORT    basic-host HTTP port (default: 8080)
#   SANDBOX_PORT    basic-host sandbox port (default: 8081)
#   FIXTURE_PORT    mcpkit fixture port (default: 3101)
#   EXAMPLE         Upstream example folder name (default: basic-server-vanillajs)
#   VERBOSE         Set to 1 for verbose playwright reporter

set -euo pipefail

EXT_APPS_DIR="${EXT_APPS_DIR:-/tmp/ext-apps}"
EXT_APPS_REPO="https://github.com/modelcontextprotocol/ext-apps.git"
HARNESS_PORT="${HARNESS_PORT:-8080}"
SANDBOX_PORT="${SANDBOX_PORT:-8081}"
FIXTURE_PORT="${FIXTURE_PORT:-3101}"
EXAMPLE="${EXAMPLE:-basic-server-vanillajs}"

FIXTURE_PID=""
HARNESS_PID=""

cleanup() {
    if [ -n "$FIXTURE_PID" ]; then
        kill "$FIXTURE_PID" 2>/dev/null || true
        wait "$FIXTURE_PID" 2>/dev/null || true
    fi
    if [ -n "$HARNESS_PID" ]; then
        kill "$HARNESS_PID" 2>/dev/null || true
        wait "$HARNESS_PID" 2>/dev/null || true
    fi
    # basic-host's bun process spawns children — sweep the ports
    for p in "$HARNESS_PORT" "$SANDBOX_PORT" "$FIXTURE_PORT"; do
        if lsof -ti:"$p" >/dev/null 2>&1; then
            lsof -ti:"$p" | xargs kill -9 2>/dev/null || true
        fi
    done
}
trap cleanup EXIT

# --- Prerequisites ----------------------------------------------------------

for cmd in npx bun go; do
    if ! command -v "$cmd" &>/dev/null; then
        echo "ERROR: $cmd not found. Install before running."
        exit 1
    fi
done

# --- Map upstream EXAMPLE → mcpkit fixture path -----------------------------

case "$EXAMPLE" in
    basic-server-vanillajs)
        FIXTURE_DIR="examples/apps/compat/basic-vanillajs"
        # Server-name regex playwright uses to scope to this example only,
        # skipping the dozens of pdf-* / other server tests that run regardless
        # of EXAMPLE env var (they live in separate .spec.ts files).
        GREP_PATTERN="Vanilla JS"
        ;;
    *)
        echo "ERROR: no mcpkit fixture for upstream example '$EXAMPLE'"
        echo "Available fixtures under examples/apps/compat/:"
        ls examples/apps/compat/ 2>/dev/null || echo "  (none — only basic-vanillajs is implemented so far)"
        exit 1
        ;;
esac

# --- Clone or update upstream -----------------------------------------------

if [ -d "$EXT_APPS_DIR/.git" ]; then
    echo "Updating ext-apps in $EXT_APPS_DIR..."
    (cd "$EXT_APPS_DIR" && git pull --quiet) || true
else
    echo "Cloning ext-apps to $EXT_APPS_DIR..."
    git clone --quiet "$EXT_APPS_REPO" "$EXT_APPS_DIR"
fi

# --- Install + build upstream pieces we need --------------------------------
# Top-level install establishes workspaces; basic-host's start script does
# `npm run build` which produces the harness HTML on serve.

echo "Installing upstream npm deps..."
(cd "$EXT_APPS_DIR" && npm install --silent --no-audit --no-fund 2>&1 | tail -5)

echo "Installing Playwright Chromium..."
(cd "$EXT_APPS_DIR" && npx playwright install --with-deps chromium 2>&1 | tail -3) || {
    echo "ERROR: playwright install failed"
    exit 1
}

echo "Building $EXAMPLE (for dist/mcp-app.html)..."
(cd "$EXT_APPS_DIR/examples/$EXAMPLE" && npm run build 2>&1 | tail -3)

# --- Write the local playwright config that bypasses upstream's webServer ---
# Upstream's playwright.config.ts starts ALL example servers + basic-host via
# `npm run examples:start`. We manage servers ourselves, so we strip the
# webServer block while inheriting everything else (testDir, reporters,
# snapshot config, etc.).

LOCAL_CONFIG="$EXT_APPS_DIR/playwright.config.mcpkit.ts"
cat > "$LOCAL_CONFIG" <<'EOF'
import baseConfig from "./playwright.config";

const { webServer, ...rest } = baseConfig as any;

export default {
    ...rest,
    // webServer omitted — caller starts basic-host + fixture externally
};
EOF

# --- Build the mcpkit fixture binary ----------------------------------------

echo "Building mcpkit fixture: $FIXTURE_DIR..."
FIXTURE_BIN="/tmp/mcpkit-fixture-$(basename "$FIXTURE_DIR")"
(cd "$FIXTURE_DIR" && go build -o "$FIXTURE_BIN" .)

# --- Start fixture (mcpkit) -------------------------------------------------

if lsof -ti:"$FIXTURE_PORT" >/dev/null 2>&1; then
    echo "Killing stale process on fixture port $FIXTURE_PORT..."
    lsof -ti:"$FIXTURE_PORT" | xargs kill -9 2>/dev/null || true
    sleep 1
fi

echo "Starting mcpkit fixture on port $FIXTURE_PORT..."
EXT_APPS_DIR="$EXT_APPS_DIR" PORT="$FIXTURE_PORT" "$FIXTURE_BIN" > /tmp/mcpkit-fixture.log 2>&1 &
FIXTURE_PID=$!

# Wait for fixture readiness
for i in $(seq 1 20); do
    if curl -sf -X POST "http://localhost:$FIXTURE_PORT/mcp" \
        -H "Content-Type: application/json" \
        -H "Accept: application/json, text/event-stream" \
        -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"probe","version":"0"}}}' \
        -o /dev/null 2>/dev/null; then
        echo "Fixture ready on :$FIXTURE_PORT"
        break
    fi
    if [ "$i" -eq 20 ]; then
        echo "ERROR: fixture failed to start. Log:"
        tail -20 /tmp/mcpkit-fixture.log
        exit 1
    fi
    sleep 1
done

# --- Start basic-host (upstream harness) ------------------------------------

for p in "$HARNESS_PORT" "$SANDBOX_PORT"; do
    if lsof -ti:"$p" >/dev/null 2>&1; then
        echo "Killing stale process on harness port $p..."
        lsof -ti:"$p" | xargs kill -9 2>/dev/null || true
    fi
done
sleep 1

echo "Starting basic-host on $HARNESS_PORT (sandbox $SANDBOX_PORT), SERVERS pointing at fixture..."
SERVERS_JSON="[\"http://localhost:$FIXTURE_PORT/mcp\"]"
(
    cd "$EXT_APPS_DIR/examples/basic-host"
    SERVERS="$SERVERS_JSON" \
    HOST_PORT="$HARNESS_PORT" \
    SANDBOX_PORT="$SANDBOX_PORT" \
    npm run start > /tmp/basic-host.log 2>&1
) &
HARNESS_PID=$!

# basic-host's `npm run start` does build (slow) + serve. Wait for the host
# port to start responding.
for i in $(seq 1 60); do
    if curl -sf "http://localhost:$HARNESS_PORT/" -o /dev/null 2>/dev/null; then
        echo "basic-host ready on :$HARNESS_PORT"
        break
    fi
    if [ "$i" -eq 60 ]; then
        echo "ERROR: basic-host failed to start within 60s. Log:"
        tail -30 /tmp/basic-host.log
        exit 1
    fi
    sleep 1
done

# --- Run upstream Playwright tests against our fixture ----------------------

PLAYWRIGHT_ARGS=""
if [ "${VERBOSE:-}" = "1" ]; then
    PLAYWRIGHT_ARGS="--reporter=list"
fi

echo ""
echo "=== Running upstream Playwright tests against mcpkit fixture ==="
echo "Example:  $EXAMPLE"
echo "Fixture:  http://localhost:$FIXTURE_PORT/mcp"
echo "Harness:  http://localhost:$HARNESS_PORT"
echo ""

set +e
(
    cd "$EXT_APPS_DIR"
    EXAMPLE="$EXAMPLE" npx playwright test \
        --config=playwright.config.mcpkit.ts \
        --grep "$GREP_PATTERN" \
        $PLAYWRIGHT_ARGS
)
EXIT_CODE=$?
set -e

echo ""
if [ $EXIT_CODE -eq 0 ]; then
    echo "=== PASSED ($EXAMPLE against mcpkit fixture) ==="
else
    echo "=== FAILED ($EXAMPLE against mcpkit fixture, exit $EXIT_CODE) ==="
fi

exit $EXIT_CODE
