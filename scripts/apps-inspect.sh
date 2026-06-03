#!/usr/bin/env bash
# Sibling to apps-demo.sh — boots upstream's TS server for an ext-apps
# example, then opens MCPJam Inspector instead of basic-host. Where
# demo-app shows you the *rendered App* (full basic-host + sandbox iframe +
# bridge protocol), inspect-app shows you the *protocol surface*: raw
# tools/list JSON, _meta.ui structure, tool-call payloads, resource list.
# Useful for cross-checking what mcpkit's drop-in fixture is actually
# putting on the wire, or for poking at tools individually.
#
# Usage:
#   make inspect-app EXAMPLE=<name> [OPEN=0]
#
# Env vars:
#   EXT_APPS_DIR    Path to ext-apps checkout (default: /tmp/ext-apps)
#   SERVER_PORT     upstream TS server port (default: 3101)
#   EXAMPLE         Upstream example folder name (required)
#   OPEN            Set to 0 to skip auto-opening MCPJam (CI / headless)
#
# Runs in the foreground; Ctrl-C tears the server down.

set -euo pipefail

EXT_APPS_DIR="${EXT_APPS_DIR:-/tmp/ext-apps}"
EXT_APPS_REPO="https://github.com/modelcontextprotocol/ext-apps.git"
SERVER_PORT="${SERVER_PORT:-3101}"
EXAMPLE="${EXAMPLE:-}"
OPEN="${OPEN:-1}"
MCPJAM_URL="https://www.mcpjam.com/"

if [ -z "$EXAMPLE" ]; then
    cat <<HELP
Usage:
  make inspect-app EXAMPLE=<name> [OPEN=0]

What it does:
  Boots upstream's TS server for an ext-apps example and opens MCPJam
  Inspector at $MCPJAM_URL — a generic MCP inspector that surfaces the
  raw protocol (tools/list JSON, _meta.ui structure, tool-call payloads).
  Where `make demo-app` shows the rendered App, this shows the wire.

Examples:
  make inspect-app EXAMPLE=basic-server-vanillajs
  make inspect-app EXAMPLE=integration-server
  OPEN=0 make inspect-app EXAMPLE=quickstart       # don't auto-open

Env vars (with defaults):
  EXT_APPS_DIR=/tmp/ext-apps    where ext-apps is cloned / will be cloned
  SERVER_PORT=3101              upstream TS server port
  OPEN=1                        auto-open MCPJam; set to 0 to skip

HELP
    if [ -d "$EXT_APPS_DIR/examples" ]; then
        echo "Available examples (from $EXT_APPS_DIR/examples/):"
        for d in "$EXT_APPS_DIR/examples/"*/; do
            name="$(basename "$d")"
            if [ "$name" = "basic-host" ]; then continue; fi
            echo "  $name"
        done
    fi
    exit 0
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

if [ -f "$EXAMPLE_DIR/dist/index.js" ]; then
    SERVER_CMD="node dist/index.js"
elif [ -f "$EXAMPLE_DIR/main.ts" ]; then
    SERVER_CMD="npx tsx main.ts"
else
    echo "ERROR: don't know how to start $EXAMPLE — no dist/index.js or main.ts"
    exit 1
fi

# --- Cleanup ----------------------------------------------------------------

SERVER_PID=""

cleanup() {
    echo ""
    echo "Shutting down..."
    if [ -n "$SERVER_PID" ]; then
        kill "$SERVER_PID" 2>/dev/null || true
        wait "$SERVER_PID" 2>/dev/null || true
    fi
    if lsof -ti:"$SERVER_PORT" >/dev/null 2>&1; then
        lsof -ti:"$SERVER_PORT" | xargs kill -9 2>/dev/null || true
    fi
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
    PORT="$SERVER_PORT" sh -c "$SERVER_CMD" > /tmp/apps-inspect-server.log 2>&1
) &
SERVER_PID=$!

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
        tail -30 /tmp/apps-inspect-server.log
        exit 1
    fi
    sleep 1
done

SERVER_URL="http://localhost:$SERVER_PORT/mcp"

cat <<BANNER

====================================================================
 $EXAMPLE is serving an MCP endpoint at:
   $SERVER_URL

 MCPJam Inspector: $MCPJAM_URL

 Once MCPJam loads:
   1. Click "Add Server" (or "Connect to MCP Server")
   2. Paste the URL above  →  $SERVER_URL
   3. Pick "Streamable HTTP" as the transport
   4. Once connected, browse the left nav:

      Tools         →  see the example's tools/list. Note the
                       _meta.ui.resourceUri pointing at the App's
                       HTML, the visibility array, output schemas, etc.
      Resources     →  ui://<name>/mcp-app.html is the App's iframe
                       HTML. Click "Read" to see the bytes (this is
                       what basic-host fetches and renders).
      Logs / Events →  watch the JSON-RPC frames as you make calls.

   5. Pick a tool, fill any required args, click "Call". The response
      shows you exactly what's on the wire — text + structured content
      + any tool-level _meta.

 What this is testing (vs make demo-app):
   demo-app    → drives basic-host, RENDERS the App iframe + drives the
                 bridge postMessage protocol (the user-facing App).
   inspect-app → shows you the protocol SURFACE — tools/list JSON,
                 _meta.ui structure, tool-call payloads, resource bytes.
                 Useful for cross-checking what mcpkit's drop-in fixture
                 puts on the wire, or for poking at tools individually.

 Logs:
   TS server:  /tmp/apps-inspect-server.log

 Press Ctrl-C to stop.
====================================================================

BANNER

if [ "$OPEN" = "1" ]; then
    if command -v open >/dev/null 2>&1; then
        open "$MCPJAM_URL"
    elif command -v xdg-open >/dev/null 2>&1; then
        xdg-open "$MCPJAM_URL" >/dev/null 2>&1 &
    fi
fi

# Wait forever (until Ctrl-C triggers cleanup)
wait $SERVER_PID
