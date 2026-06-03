#!/usr/bin/env bash
# Spin up upstream's ext-apps TS server + basic-host for a single example so
# you can browse it interactively in a real browser. Useful for:
#
#   - SKIP examples (video-resource-server, lazy-auth-server) that aren't in
#     upstream's servers.spec.ts so the Playwright wrapper can't drive them
#   - Any example you just want to look at without driving the test suite
#   - Cross-checking what upstream's own implementation renders vs. our
#     mcpkit-Go drop-in (run the demo for upstream's TS, run our compat
#     fixture in a second terminal on a different port)
#
# Pure browse target. No Docker. No Playwright. No drift check. No snapshots.
# Just upstream's stack + a URL.
#
# Usage:
#   make demo-app EXAMPLE=video-resource-server
#   make demo-app EXAMPLE=lazy-auth-server
#   make demo-app EXAMPLE=basic-server-vanillajs       # works for testable ones too
#
# Env:
#   EXT_APPS_DIR    Path to ext-apps checkout (default: /tmp/ext-apps)
#   HARNESS_PORT    basic-host HTTP port (default: 8080)
#   SANDBOX_PORT    basic-host sandbox port (default: 8081)
#   SERVER_PORT     upstream example server port (default: 3101)
#   EXAMPLE         Upstream example folder name (required; e.g. "video-resource-server")
#   OPEN            Set to 1 to auto-open the browser (default: just print URL)
#
# Runs in the foreground and tears the processes down on Ctrl-C.

set -euo pipefail

EXT_APPS_DIR="${EXT_APPS_DIR:-/tmp/ext-apps}"
EXT_APPS_REPO="https://github.com/modelcontextprotocol/ext-apps.git"
HARNESS_PORT="${HARNESS_PORT:-8080}"
SANDBOX_PORT="${SANDBOX_PORT:-8081}"
SERVER_PORT="${SERVER_PORT:-3101}"
EXAMPLE="${EXAMPLE:-}"
OPEN="${OPEN:-}"

if [ -z "$EXAMPLE" ]; then
    echo "ERROR: EXAMPLE is required. Set to an upstream ext-apps example folder name."
    echo ""
    echo "Examples:"
    echo "  make demo-app EXAMPLE=video-resource-server"
    echo "  make demo-app EXAMPLE=lazy-auth-server"
    echo "  make demo-app EXAMPLE=basic-server-vanillajs"
    exit 1
fi

# --- Prerequisites ----------------------------------------------------------

for cmd in npx bun; do
    if ! command -v "$cmd" &>/dev/null; then
        echo "ERROR: $cmd not found. Install before running."
        exit 1
    fi
done

# --- Clone or update upstream -----------------------------------------------

if [ -d "$EXT_APPS_DIR/.git" ]; then
    echo "Updating ext-apps in $EXT_APPS_DIR..."
    (cd "$EXT_APPS_DIR" && git pull --quiet) || true
else
    echo "Cloning ext-apps to $EXT_APPS_DIR..."
    git clone --quiet "$EXT_APPS_REPO" "$EXT_APPS_DIR"
fi

EXAMPLE_DIR="$EXT_APPS_DIR/examples/$EXAMPLE"
if [ ! -d "$EXAMPLE_DIR" ]; then
    echo "ERROR: upstream example '$EXAMPLE' not found at $EXAMPLE_DIR"
    echo ""
    echo "Available examples:"
    ls "$EXT_APPS_DIR/examples/" 2>/dev/null | sed 's/^/  /'
    exit 1
fi

# --- npm install + build the example ----------------------------------------

if [ ! -d "$EXT_APPS_DIR/node_modules/@playwright" ]; then
    echo "Installing upstream npm deps (cold)..."
    (cd "$EXT_APPS_DIR" && npm install --silent --no-audit --no-fund 2>&1 | tail -3)
fi

echo "Building $EXAMPLE..."
(cd "$EXAMPLE_DIR" && npm run build 2>&1 | tail -5)

# --- Decide how to start the upstream server --------------------------------
# Some examples ship a built dist/index.js (via `bun build main.ts --outfile
# dist/index.js`); others (quickstart, lazy-auth, ...) only build the iframe
# and expect `tsx main.ts` to run the server directly.

if [ -f "$EXAMPLE_DIR/dist/index.js" ]; then
    SERVER_CMD="node dist/index.js"
elif [ -f "$EXAMPLE_DIR/main.ts" ]; then
    SERVER_CMD="npx tsx main.ts"
else
    echo "ERROR: don't know how to start $EXAMPLE — no dist/index.js or main.ts"
    exit 1
fi

# --- Cleanup ---------------------------------------------------------------

SERVER_PID=""
HARNESS_PID=""

cleanup() {
    echo ""
    echo "Shutting down..."
    if [ -n "$SERVER_PID" ]; then
        kill "$SERVER_PID" 2>/dev/null || true
        wait "$SERVER_PID" 2>/dev/null || true
    fi
    if [ -n "$HARNESS_PID" ]; then
        kill "$HARNESS_PID" 2>/dev/null || true
        wait "$HARNESS_PID" 2>/dev/null || true
    fi
    # basic-host's bun process spawns children — sweep the ports
    for p in "$HARNESS_PORT" "$SANDBOX_PORT" "$SERVER_PORT"; do
        if lsof -ti:"$p" >/dev/null 2>&1; then
            lsof -ti:"$p" | xargs kill -9 2>/dev/null || true
        fi
    done
}
trap cleanup EXIT INT TERM

# --- Start upstream's TS server ---------------------------------------------

if lsof -ti:"$SERVER_PORT" >/dev/null 2>&1; then
    echo "Killing stale process on server port $SERVER_PORT..."
    lsof -ti:"$SERVER_PORT" | xargs kill -9 2>/dev/null || true
    sleep 1
fi

echo "Starting $EXAMPLE TS server on :$SERVER_PORT ($SERVER_CMD)..."
(
    cd "$EXAMPLE_DIR"
    PORT="$SERVER_PORT" sh -c "$SERVER_CMD" > /tmp/apps-demo-server.log 2>&1
) &
SERVER_PID=$!

# Wait for readiness
for i in $(seq 1 30); do
    if curl -sf -X POST "http://localhost:$SERVER_PORT/mcp" \
        -H "Content-Type: application/json" \
        -H "Accept: application/json, text/event-stream" \
        -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"probe","version":"0"}}}' \
        -o /dev/null 2>/dev/null; then
        echo "TS server ready on :$SERVER_PORT"
        break
    fi
    if [ "$i" -eq 30 ]; then
        echo "ERROR: TS server failed to start. Log:"
        tail -30 /tmp/apps-demo-server.log
        exit 1
    fi
    sleep 1
done

# --- Start basic-host -------------------------------------------------------

for p in "$HARNESS_PORT" "$SANDBOX_PORT"; do
    if lsof -ti:"$p" >/dev/null 2>&1; then
        echo "Killing stale process on harness port $p..."
        lsof -ti:"$p" | xargs kill -9 2>/dev/null || true
    fi
done
sleep 1

echo "Starting basic-host on :$HARNESS_PORT (sandbox :$SANDBOX_PORT)..."
SERVERS_JSON="[\"http://localhost:$SERVER_PORT/mcp\"]"
(
    cd "$EXT_APPS_DIR/examples/basic-host"
    SERVERS="$SERVERS_JSON" \
    HOST_PORT="$HARNESS_PORT" \
    SANDBOX_PORT="$SANDBOX_PORT" \
    npm run start > /tmp/apps-demo-harness.log 2>&1
) &
HARNESS_PID=$!

for i in $(seq 1 60); do
    if curl -sf "http://localhost:$HARNESS_PORT/" -o /dev/null 2>/dev/null; then
        echo "basic-host ready on :$HARNESS_PORT"
        break
    fi
    if [ "$i" -eq 60 ]; then
        echo "ERROR: basic-host failed to start within 60s. Log:"
        tail -30 /tmp/apps-demo-harness.log
        exit 1
    fi
    sleep 1
done

URL="http://localhost:$HARNESS_PORT"

echo ""
echo "===================================================================="
echo " $EXAMPLE is now serving."
echo " Open in your browser:  $URL"
echo ""
echo " Logs:"
echo "   TS server:  /tmp/apps-demo-server.log"
echo "   basic-host: /tmp/apps-demo-harness.log"
echo ""
echo " Press Ctrl-C to stop."
echo "===================================================================="
echo ""

if [ "$OPEN" = "1" ]; then
    if command -v open >/dev/null 2>&1; then
        open "$URL"
    elif command -v xdg-open >/dev/null 2>&1; then
        xdg-open "$URL" >/dev/null 2>&1 &
    fi
fi

# Wait forever (until Ctrl-C triggers cleanup)
wait $HARNESS_PID
