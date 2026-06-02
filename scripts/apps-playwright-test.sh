#!/usr/bin/env bash
# Run upstream's ext-apps Playwright suite against a mcpkit-Go drop-in.
#
# Two modes:
#
#   1. Native (default) — fixture + basic-host + Playwright run on the host.
#      Fast iteration; visual checks compare against the host OS's committed
#      baseline (e.g. examples/apps/compat/<fixture>/__snapshots__/<key>-darwin.png).
#
#   2. Docker (DOCKER=1) — fixture cross-compiled for linux/amd64 on the host,
#      then everything runs inside mcr.microsoft.com/playwright:v1.57.0-noble.
#      Snapshots produced are byte-identical to what upstream's own CI image
#      generates. Use this to (re)generate the canonical linux baseline.
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
#   - Native mode: Node.js 22+ with npx, bun, Go
#   - Docker mode: docker, Go (host-side cross-compile only)
#
# Usage:
#   make test-apps-playwright              # native mode
#   DOCKER=1 make test-apps-playwright     # CI-identical Docker mode
#   make test-apps-playwright-docker       # alias for DOCKER=1
#
# Environment:
#   EXT_APPS_DIR       Path to ext-apps checkout (default: /tmp/ext-apps)
#   HARNESS_PORT       basic-host HTTP port (default: 8080)
#   SANDBOX_PORT       basic-host sandbox port (default: 8081)
#   FIXTURE_PORT       mcpkit fixture port (default: 3101)
#   EXAMPLE            Upstream example folder name (default: basic-server-vanillajs)
#   VERBOSE            Set to 1 for verbose playwright reporter
#   UPDATE_SNAPSHOTS   Set to 1 to (re)generate the baseline PNG for the
#                      current platform. Snapshot filenames are suffixed with
#                      the platform name (-darwin / -linux), so docker-mode
#                      regeneration does NOT overwrite native-mode baselines.
#   DOCKER             Set to 1 to run everything inside upstream's Playwright
#                      image (mcr.microsoft.com/playwright:v1.57.0-noble) for
#                      CI-identical baselines.

set -euo pipefail

EXT_APPS_DIR="${EXT_APPS_DIR:-/tmp/ext-apps}"
EXT_APPS_REPO="https://github.com/modelcontextprotocol/ext-apps.git"
HARNESS_PORT="${HARNESS_PORT:-8080}"
SANDBOX_PORT="${SANDBOX_PORT:-8081}"
FIXTURE_PORT="${FIXTURE_PORT:-3101}"
EXAMPLE="${EXAMPLE:-basic-server-vanillajs}"
UPDATE_SNAPSHOTS="${UPDATE_SNAPSHOTS:-}"
DOCKER="${DOCKER:-}"
DOCKER_IMAGE="mcr.microsoft.com/playwright:v1.57.0-noble"

# Absolute path to this repo root — needed because we generate playwright config
# inside the ext-apps tree but want snapshots to resolve back to our tree.
MCPKIT_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# --- Prerequisites ----------------------------------------------------------

REQUIRED_CMDS=(go)
if [ "$DOCKER" = "1" ]; then
    REQUIRED_CMDS+=(docker)
else
    REQUIRED_CMDS+=(npx bun)
fi
for cmd in "${REQUIRED_CMDS[@]}"; do
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

# Mcpkit-local snapshot dir. Lives under the fixture so each compat fixture
# owns its own baseline and we never touch upstream's tests/e2e/.*-snapshots/
# tree.
SNAPSHOT_DIR_ABS="$MCPKIT_ROOT/$FIXTURE_DIR/__snapshots__"
mkdir -p "$SNAPSHOT_DIR_ABS"

# Container-side view of the snapshot dir (only used when DOCKER=1).
SNAPSHOT_DIR_CONTAINER="/mcpkit/$FIXTURE_DIR/__snapshots__"

# --- Clone or update upstream (native only) ---------------------------------
# Docker mode clones into a container-side named volume — the inner script
# handles that — so the host's $EXT_APPS_DIR stays untouched (avoids
# cross-platform node_modules contamination).

if [ "$DOCKER" != "1" ]; then
    if [ -d "$EXT_APPS_DIR/.git" ]; then
        echo "Updating ext-apps in $EXT_APPS_DIR..."
        (cd "$EXT_APPS_DIR" && git pull --quiet) || true
    else
        echo "Cloning ext-apps to $EXT_APPS_DIR..."
        git clone --quiet "$EXT_APPS_REPO" "$EXT_APPS_DIR"
    fi
fi

# --- Write the local playwright config (native mode only) -------------------
# Upstream's playwright.config.ts starts ALL example servers + basic-host via
# `npm run examples:start`. We manage servers ourselves, so we strip the
# webServer block while inheriting everything else (testDir, reporters,
# snapshot config, etc.).
#
# The snapshot path resolves at runtime from MCPKIT_SNAPSHOT_DIR, with the
# platform suffix ({platform} → "darwin" / "linux") keeping the two baselines
# on disk side by side.
#
# Docker mode writes its own copy of this config inside the container volume
# (see apps-playwright-docker-inner.sh) — the host's ext-apps tree may not
# even exist in docker mode.

if [ "$DOCKER" != "1" ]; then
    cat > "$EXT_APPS_DIR/playwright.config.mcpkit.ts" <<EOF
import baseConfig from "./playwright.config";

const { webServer, ...rest } = baseConfig as any;

const snapshotDir =
    process.env.MCPKIT_SNAPSHOT_DIR ?? "$SNAPSHOT_DIR_ABS";

export default {
    ...rest,
    // webServer omitted — caller starts basic-host + fixture externally.
    // snapshotPathTemplate points at the mcpkit repo's per-fixture baseline,
    // with the {platform} token suffixed so darwin + linux baselines coexist
    // (visible as basic-vanillajs-darwin.png / basic-vanillajs-linux.png).
    snapshotPathTemplate: \`\${snapshotDir}/{arg}-{platform}{ext}\`,
};
EOF
fi

# --- Mode dispatch ----------------------------------------------------------

if [ "$DOCKER" = "1" ]; then
    # ------------------------------------------------------------------ Docker
    # Cross-compile the Go fixture on the host so the container doesn't need
    # Go installed. The binary is mounted into the container alongside the
    # mcpkit repo and ext-apps tree.

    FIXTURE_BIN_HOST="$MCPKIT_ROOT/.tmp-fixture-linux-amd64-$(basename "$FIXTURE_DIR")"
    FIXTURE_BIN_CONTAINER="/tmp/fixture-linux-amd64"
    trap 'rm -f "$FIXTURE_BIN_HOST"' EXIT

    echo "Cross-compiling fixture for linux/amd64..."
    (cd "$FIXTURE_DIR" && GOOS=linux GOARCH=amd64 go build -o "$FIXTURE_BIN_HOST" .)

    echo "Pulling $DOCKER_IMAGE if needed..."
    docker pull --quiet "$DOCKER_IMAGE" 2>&1 | tail -3 || true

    # Named volume keeps ext-apps + its node_modules entirely in Docker — the
    # host's tree at $EXT_APPS_DIR is never touched, so cross-platform module
    # contamination (darwin-arm64 vs linux-x64 rollup, etc.) is impossible.
    # The volume persists across runs, caching the clone + npm install.
    DOCKER_VOLUME="mcpkit-ext-apps"

    echo ""
    echo "=== Launching $DOCKER_IMAGE (volume: $DOCKER_VOLUME) ==="
    docker run --rm \
        -e EXAMPLE="$EXAMPLE" \
        -e GREP_PATTERN="$GREP_PATTERN" \
        -e FIXTURE_BIN="$FIXTURE_BIN_CONTAINER" \
        -e MCPKIT_SNAPSHOT_DIR="$SNAPSHOT_DIR_CONTAINER" \
        -e HARNESS_PORT="$HARNESS_PORT" \
        -e SANDBOX_PORT="$SANDBOX_PORT" \
        -e FIXTURE_PORT="$FIXTURE_PORT" \
        -e EXT_APPS_DIR=/ext-apps \
        -e EXT_APPS_REPO="$EXT_APPS_REPO" \
        -e UPDATE_SNAPSHOTS="$UPDATE_SNAPSHOTS" \
        -e VERBOSE="${VERBOSE:-}" \
        -v "$MCPKIT_ROOT":/mcpkit \
        -v "$DOCKER_VOLUME":/ext-apps \
        -v "$FIXTURE_BIN_HOST":"$FIXTURE_BIN_CONTAINER":ro \
        "$DOCKER_IMAGE" \
        bash /mcpkit/scripts/apps-playwright-docker-inner.sh
    EXIT_CODE=$?
else
    # ------------------------------------------------------------------ Native
    # Heads-up before the first run if your platform's baseline isn't committed
    # yet — saves a confused-debugging loop.
    PLATFORM_LOWER="$(uname -s | tr '[:upper:]' '[:lower:]')"
    case "$PLATFORM_LOWER" in
        darwin) EXPECTED_PLATFORM="darwin" ;;
        linux)  EXPECTED_PLATFORM="linux"  ;;
        *)      EXPECTED_PLATFORM=""       ;;
    esac
    if [ -n "$EXPECTED_PLATFORM" ] && \
       [ "$UPDATE_SNAPSHOTS" != "1" ] && \
       [ -d "$SNAPSHOT_DIR_ABS" ] && \
       ! ls "$SNAPSHOT_DIR_ABS"/*-"$EXPECTED_PLATFORM".png >/dev/null 2>&1; then
        echo ""
        echo "WARN: no committed baseline for platform '$EXPECTED_PLATFORM' under"
        echo "      $SNAPSHOT_DIR_ABS"
        echo "      Visual checks will fail until you run:"
        echo "        UPDATE_SNAPSHOTS=1 $0"
        echo "      to generate one (or DOCKER=1 UPDATE_SNAPSHOTS=1 to regenerate"
        echo "      the canonical linux baseline)."
        echo ""
    fi

    # --- Install + build upstream pieces we need ---------------------------
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

    # --- Build the mcpkit fixture binary -----------------------------------

    FIXTURE_PID=""
    HARNESS_PID=""

    cleanup_native() {
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
    trap cleanup_native EXIT

    echo "Building mcpkit fixture: $FIXTURE_DIR..."
    FIXTURE_BIN="/tmp/mcpkit-fixture-$(basename "$FIXTURE_DIR")"
    (cd "$FIXTURE_DIR" && go build -o "$FIXTURE_BIN" .)

    # --- Start fixture (mcpkit) --------------------------------------------

    if lsof -ti:"$FIXTURE_PORT" >/dev/null 2>&1; then
        echo "Killing stale process on fixture port $FIXTURE_PORT..."
        lsof -ti:"$FIXTURE_PORT" | xargs kill -9 2>/dev/null || true
        sleep 1
    fi

    echo "Starting mcpkit fixture on port $FIXTURE_PORT..."
    EXT_APPS_DIR="$EXT_APPS_DIR" PORT="$FIXTURE_PORT" "$FIXTURE_BIN" > /tmp/mcpkit-fixture.log 2>&1 &
    FIXTURE_PID=$!

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

    # --- Start basic-host (upstream harness) -------------------------------

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

    # --- Run upstream Playwright tests against our fixture -----------------

    PLAYWRIGHT_ARGS=""
    if [ "${VERBOSE:-}" = "1" ]; then
        PLAYWRIGHT_ARGS="--reporter=list"
    fi
    if [ "$UPDATE_SNAPSHOTS" = "1" ]; then
        PLAYWRIGHT_ARGS="$PLAYWRIGHT_ARGS --update-snapshots"
    fi

    echo ""
    echo "=== Running upstream Playwright tests against mcpkit fixture (native) ==="
    echo "Example:    $EXAMPLE"
    echo "Fixture:    http://localhost:$FIXTURE_PORT/mcp"
    echo "Harness:    http://localhost:$HARNESS_PORT"
    echo "Snapshots:  $SNAPSHOT_DIR_ABS  (suffix: -$EXPECTED_PLATFORM)"
    if [ "$UPDATE_SNAPSHOTS" = "1" ]; then
        echo "MODE:       --update-snapshots (regenerating baseline for -$EXPECTED_PLATFORM)"
    fi
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
fi

echo ""
if [ "$EXIT_CODE" -eq 0 ]; then
    echo "=== PASSED ($EXAMPLE against mcpkit fixture${DOCKER:+, docker}) ==="
else
    echo "=== FAILED ($EXAMPLE against mcpkit fixture${DOCKER:+, docker}, exit $EXIT_CODE) ==="
fi

exit $EXIT_CODE
