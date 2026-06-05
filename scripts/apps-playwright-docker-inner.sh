#!/usr/bin/env bash
# Inner script for DOCKER=1 mode of apps-playwright-test.sh — runs INSIDE
# mcr.microsoft.com/playwright:v1.57.0-noble (Ubuntu Noble + Node + Playwright +
# Chromium pre-installed). Outer script handles the host-side setup
# (cross-compile, clone, mount, docker run) and invokes this with env vars
# populated.
#
# Required env vars (passed through `docker run -e`):
#   EXAMPLE              upstream example folder name (e.g. basic-server-vanillajs)
#   GREP_PATTERN         regex playwright uses to scope to the matching describe
#   FIXTURE_BIN          path inside the container to the linux/amd64 binary
#   MCPKIT_SNAPSHOT_DIR  container path for __snapshots__/ (read by config)
#   HARNESS_PORT         basic-host port
#   SANDBOX_PORT         basic-host sandbox port
#   FIXTURE_PORT         fixture port
#   EXT_APPS_DIR         container mount of ext-apps (e.g. /ext-apps)
#   UPDATE_SNAPSHOTS     "1" to pass --update-snapshots
#   VERBOSE              "1" for --reporter=list

set -euo pipefail

# --- Clone or update ext-apps inside the container --------------------------
# Outer script mounts /ext-apps as a Docker named volume (not a host bind), so
# the host's $EXT_APPS_DIR tree is never touched. First docker run clones into
# the empty volume; subsequent runs do a pull. The volume also caches
# node_modules between runs so we only pay the install cost once.

EXT_APPS_REPO="${EXT_APPS_REPO:-https://github.com/modelcontextprotocol/ext-apps.git}"
if [ -d "$EXT_APPS_DIR/.git" ]; then
    echo "Updating ext-apps inside container..."
    # Discard any dirty state left by a prior fixture's run in this shared
    # volume — `npm install` and example builds can dirty package-lock.json
    # and dist/ entries. Without this, `git pull` fails with "would be
    # overwritten by merge" and subsequent fixtures inherit an inconsistent
    # tree. Issue 601 / PR 596 --all reliability fix.
    (cd "$EXT_APPS_DIR" && git reset --hard HEAD --quiet && git clean -fd --quiet) || true
    (cd "$EXT_APPS_DIR" && git pull --quiet) || true
else
    echo "Cloning ext-apps into container volume..."
    git clone --quiet "$EXT_APPS_REPO" "$EXT_APPS_DIR"
fi

# --- Container-side dependencies --------------------------------------------
# The base image ships node + npm + playwright + chromium. Upstream's
# basic-host build script uses bun, so install that. uv/python3 are upstream's
# concerns for examples we don't drive (say-server, etc.) and are skipped.

if ! command -v bun >/dev/null 2>&1; then
    echo "Installing bun..."
    npm install -g bun --silent 2>&1 | tail -3
fi

# node_modules lives entirely inside the named volume, so warm runs skip the
# install — no cross-platform contamination risk since the host's tree is
# never touched by docker mode.
if [ ! -d "$EXT_APPS_DIR/node_modules/@playwright" ]; then
    echo "Installing ext-apps npm deps (cold)..."
    (cd "$EXT_APPS_DIR" && npm install --silent --no-audit --no-fund 2>&1 | tail -5)
else
    echo "ext-apps node_modules cached in volume (warm)"
fi

echo "Building $EXAMPLE upstream UI (for dist/mcp-app.html)..."
(cd "$EXT_APPS_DIR/examples/$EXAMPLE" && npm run build 2>&1 | tail -10)

# --- Write the local playwright config inside the container -----------------
# Mirrors the host-side native config writer in apps-playwright-test.sh —
# strips upstream's webServer block, points snapshots + failure artifacts at
# the mcpkit repo's per-fixture dirs (via MCPKIT_SNAPSHOT_DIR /
# MCPKIT_ARTIFACTS_DIR — container paths that the /mcpkit bind-mount maps
# back to the host so test-results survive after docker exit).
cat > "$EXT_APPS_DIR/playwright.config.mcpkit.ts" <<CFG
import baseConfig from "./playwright.config";

const { webServer, ...rest } = baseConfig as any;

const snapshotDir =
    process.env.MCPKIT_SNAPSHOT_DIR ?? "$MCPKIT_SNAPSHOT_DIR";
const artifactsDir =
    process.env.MCPKIT_ARTIFACTS_DIR ?? "$MCPKIT_ARTIFACTS_DIR";

export default {
    ...rest,
    snapshotPathTemplate: \`\${snapshotDir}/{arg}{ext}\`,
    outputDir: artifactsDir,
};
CFG

# --- Start the mcpkit fixture (linux/amd64 binary cross-compiled on host) ---

echo "Starting mcpkit fixture binary $FIXTURE_BIN on :$FIXTURE_PORT..."
EXT_APPS_DIR="$EXT_APPS_DIR" PORT="$FIXTURE_PORT" "$FIXTURE_BIN" \
    > /tmp/mcpkit-fixture.log 2>&1 &
FIXTURE_PID=$!

cleanup() {
    if [ -n "${FIXTURE_PID:-}" ]; then
        kill "$FIXTURE_PID" 2>/dev/null || true
    fi
    if [ -n "${UPSTREAM_PID:-}" ]; then
        kill "$UPSTREAM_PID" 2>/dev/null || true
    fi
    if [ -n "${HARNESS_PID:-}" ]; then
        kill "$HARNESS_PID" 2>/dev/null || true
    fi
}
trap cleanup EXIT

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

# --- tools/list parity check vs upstream TS server --------------------------
# Start upstream's own basic-server-vanillajs on a side port, fetch tools/list
# from both servers, JSON-diff. Catches protocol-surface drift that the
# pixel snapshot test silently swallows when both sides regenerate against
# their own past output. SKIP_DRIFT_CHECK=1 opts out (debugging only).

if [ "${SKIP_DRIFT_CHECK:-}" = "1" ]; then
    echo "SKIP_DRIFT_CHECK=1 — skipping tools/list parity check"
else
    UPSTREAM_PORT="${UPSTREAM_PORT:-3102}"

    # Most upstream examples ship a built dist/index.js (via `bun build
    # main.ts --outfile dist/index.js`). A few (quickstart, ...) only ship
    # the iframe HTML and run the server directly from main.ts via tsx. Try
    # node first, fall back to tsx.
    UPSTREAM_DIR="$EXT_APPS_DIR/examples/$EXAMPLE"
    if [ -f "$UPSTREAM_DIR/dist/index.js" ]; then
        UPSTREAM_CMD="node dist/index.js"
    elif [ -f "$UPSTREAM_DIR/main.ts" ]; then
        UPSTREAM_CMD="npx tsx main.ts"
    else
        echo "ERROR: don't know how to start upstream server for $EXAMPLE — no dist/index.js or main.ts"
        exit 1
    fi
    # pdf-server ships two surfaces (4-tool default, 9-tool --enable-interact).
    # mcpkit fixture targets the 9-tool surface, so the drift-check upstream
    # must boot with the flag too. Issue 554.
    if [ "$EXAMPLE" = "pdf-server" ]; then
        UPSTREAM_CMD="$UPSTREAM_CMD --enable-interact"
    fi
    echo "Starting upstream TS server on :$UPSTREAM_PORT (for tools/list drift check; cmd: $UPSTREAM_CMD)..."
    (
        cd "$UPSTREAM_DIR"
        PORT="$UPSTREAM_PORT" sh -c "$UPSTREAM_CMD" > /tmp/upstream-server.log 2>&1
    ) &
    UPSTREAM_PID=$!

    for i in $(seq 1 20); do
        if curl -sf -X POST "http://localhost:$UPSTREAM_PORT/mcp" \
            -H "Content-Type: application/json" \
            -H "Accept: application/json, text/event-stream" \
            -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"probe","version":"0"}}}' \
            -o /dev/null 2>/dev/null; then
            echo "Upstream TS server ready on :$UPSTREAM_PORT"
            break
        fi
        if [ "$i" -eq 20 ]; then
            echo "ERROR: upstream TS server failed to start. Log:"
            tail -20 /tmp/upstream-server.log
            exit 1
        fi
        sleep 1
    done

    echo ""
    echo "=== tools/list parity check ==="
    # Copy the diff script into ext-apps so Node's ESM resolver walks up into
    # ext-apps' node_modules naturally (NODE_PATH only works for CJS; the
    # script imports @modelcontextprotocol/sdk as ESM).
    cp /mcpkit/scripts/apps-playwright-tools-diff.mjs "$EXT_APPS_DIR/.tools-diff.mjs"
    if ! node "$EXT_APPS_DIR/.tools-diff.mjs" \
        "mcpkit" "http://localhost:$FIXTURE_PORT/mcp" \
        "upstream" "http://localhost:$UPSTREAM_PORT/mcp"; then
        DRIFT_EXIT=$?
        echo ""
        echo "Protocol-surface drift between the mcpkit fixture and upstream's TS server."
        echo "Update the fixture under examples/apps/compat/<name>/main.go to match,"
        echo "or — if the gap is a real ext/ui library issue — file it and SKIP_DRIFT_CHECK=1"
        echo "to keep iterating while it's tracked."
        exit $DRIFT_EXIT
    fi
    echo ""

    # Drift check done — release the upstream server before basic-host starts
    # (basic-host's SERVERS env only points at the mcpkit fixture).
    kill "$UPSTREAM_PID" 2>/dev/null || true
    wait "$UPSTREAM_PID" 2>/dev/null || true
    UPSTREAM_PID=""
fi

# --- Start basic-host inside the container ----------------------------------

echo "Starting basic-host on :$HARNESS_PORT (sandbox :$SANDBOX_PORT)..."
SERVERS_JSON="[\"http://localhost:$FIXTURE_PORT/mcp\"]"
(
    cd "$EXT_APPS_DIR/examples/basic-host"
    SERVERS="$SERVERS_JSON" \
    HOST_PORT="$HARNESS_PORT" \
    SANDBOX_PORT="$SANDBOX_PORT" \
    npm run start > /tmp/basic-host.log 2>&1
) &
HARNESS_PID=$!

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

# --- Run Playwright ---------------------------------------------------------

PLAYWRIGHT_ARGS=""
if [ "${VERBOSE:-}" = "1" ]; then
    PLAYWRIGHT_ARGS="--reporter=list"
fi
if [ "${UPDATE_SNAPSHOTS:-}" = "1" ]; then
    PLAYWRIGHT_ARGS="$PLAYWRIGHT_ARGS --update-snapshots"
fi

echo ""
echo "=== Running upstream Playwright tests inside Docker ==="
echo "Image:      mcr.microsoft.com/playwright:v1.57.0-noble"
echo "Example:    $EXAMPLE"
echo "Fixture:    http://localhost:$FIXTURE_PORT/mcp"
echo "Harness:    http://localhost:$HARNESS_PORT"
echo "Snapshots:  $MCPKIT_SNAPSHOT_DIR (container path)"
if [ "${UPDATE_SNAPSHOTS:-}" = "1" ]; then
    echo "MODE:       --update-snapshots (regenerating linux baseline)"
fi
echo ""

set +e
(
    cd "$EXT_APPS_DIR"
    EXAMPLE="$EXAMPLE" \
    MCPKIT_SNAPSHOT_DIR="$MCPKIT_SNAPSHOT_DIR" \
    MCPKIT_ARTIFACTS_DIR="$MCPKIT_ARTIFACTS_DIR" \
    PLAYWRIGHT_HTML_OUTPUT_DIR="$MCPKIT_REPORT_DIR" \
    PLAYWRIGHT_HTML_OPEN=never \
        npx playwright test \
        --config=playwright.config.mcpkit.ts \
        --grep "$GREP_PATTERN" \
        $PLAYWRIGHT_ARGS
)
EXIT_CODE=$?
set -e

exit $EXIT_CODE
