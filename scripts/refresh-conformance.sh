#!/bin/bash
# Regenerates CONFORMANCE.md at the repo root by:
#   1. spawning cmd/testserver on a free port
#   2. running upstream tier-check --output json against it
#   3. driving tools/conformance-report to splice the result into CONFORMANCE.md
#
# Requires:
#   - Node.js 22+ (npx)
#   - A clone of modelcontextprotocol/conformance at $MCPCONFORMANCE_BASE_PATH
#     (defaults to ../conf-upstream-main relative to repo root)
#   - GH_TOKEN or `gh auth login` — upstream tier-check still queries GitHub
#     for tier-1 signals; the renderer drops those slices on the way out, but
#     the CLI itself fails closed without a token.
#
# Output: writes CONFORMANCE.md at the repo root in-place. Re-running on
# unchanged input must produce a byte-identical file — this is the contract
# the CI staleness gate relies on.
#
# Env overrides:
#   MCPCONFORMANCE_BASE_PATH — path to upstream conformance clone
#   REFRESH_PORT             — testserver port (default 18799)
#   REFRESH_OUT              — output path (default CONFORMANCE.md at repo root)
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CONF_DIR="${MCPCONFORMANCE_BASE_PATH:-$REPO_ROOT/../conf-upstream-main}"
PORT="${REFRESH_PORT:-18799}"
OUT="${REFRESH_OUT:-$REPO_ROOT/CONFORMANCE.md}"
WORK_DIR="$(mktemp -d -t conformance-report.XXXXXX)"
SERVER_PID=""

cleanup() {
    if [ -n "$SERVER_PID" ]; then
        kill "$SERVER_PID" 2>/dev/null || true
        wait "$SERVER_PID" 2>/dev/null || true
    fi
}
trap cleanup EXIT

if [ ! -d "$CONF_DIR" ]; then
    echo "refresh-conformance: \$MCPCONFORMANCE_BASE_PATH=$CONF_DIR does not exist." >&2
    echo "  Clone https://github.com/modelcontextprotocol/conformance there, or" >&2
    echo "  re-run with MCPCONFORMANCE_BASE_PATH=<path-to-clone>." >&2
    exit 1
fi

if ! command -v npx >/dev/null 2>&1; then
    echo "refresh-conformance: npx not found. Install Node.js 22+." >&2
    exit 1
fi

# --- 1. Build mcpkit fixture -----------------------------------------------

echo "=== Building cmd/testserver ==="
(cd "$REPO_ROOT" && go build -o "$WORK_DIR/testserver" ./cmd/testserver)

# Kill any stale process on the port to avoid connecting to old code.
if lsof -i ":$PORT" -t >/dev/null 2>&1; then
    echo "Killing stale process on port $PORT..."
    lsof -i ":$PORT" -t | xargs kill 2>/dev/null || true
    sleep 1
fi

echo "=== Spawning testserver on :$PORT ==="
STREAMABLE=1 PORT=$PORT "$WORK_DIR/testserver" > "$WORK_DIR/testserver.log" 2>&1 &
SERVER_PID=$!

# Wait for readiness via initialize handshake.
echo -n "Waiting for testserver..."
for i in $(seq 1 30); do
    if curl -s -o /dev/null -w "%{http_code}" -X POST "http://localhost:$PORT/mcp" \
        -H "Content-Type: application/json" \
        -H "Accept: application/json, text/event-stream" \
        -d '{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"refresh","version":"0.0"}}}' \
        2>/dev/null | grep -q "200"; then
        echo " ready"
        break
    fi
    echo -n "."
    sleep 1
done

if ! kill -0 "$SERVER_PID" 2>/dev/null; then
    echo " FAIL: testserver exited"
    cat "$WORK_DIR/testserver.log"
    exit 1
fi

# --- 2. Run upstream tier-check --------------------------------------------

echo ""
echo "=== Installing upstream conformance deps (builds dist/ via prepare) ==="
(cd "$CONF_DIR" && npm install --silent)

# Full 40-char SHA — actions/checkout@v4 in the CI freshness gate cannot
# resolve short SHAs as refs (it tries to fetch them as branch/tag names).
UPSTREAM_SHA="$(cd "$CONF_DIR" && git rev-parse HEAD)"
PROTOCOL_VERSION="${REFRESH_PROTOCOL:-2025-11-25}"

echo ""
echo "=== Running upstream tier-check --output json ==="
# tier-check needs a GH token for repo-side signals (labels/triage/etc.). The
# renderer drops those from CONFORMANCE.md, but the CLI itself bails without
# one. We pass --skip-conformance off so the scorecard carries scenario
# results; the only reason we run tier-check (vs running `server` directly)
# is that its JSON shape is already what the renderer parses.
(cd "$CONF_DIR" && node dist/index.js tier-check \
    --repo panyam/mcpkit \
    --conformance-server-url "http://localhost:$PORT/mcp" \
    --output json \
    > "$WORK_DIR/scorecard.json")

# --- 3. Render -------------------------------------------------------------

echo ""
echo "=== Rendering CONFORMANCE.md ==="
# Install renderer deps once; idempotent on rerun.
(cd "$REPO_ROOT/tools/conformance-report" && npm install --silent)
(cd "$REPO_ROOT/tools/conformance-report" && npx tsx src/index.ts \
    --scorecard "$WORK_DIR/scorecard.json" \
    --traceability "$CONF_DIR/src/seps/traceability.json" \
    --known-gaps "$REPO_ROOT/conformance/known-gaps.yaml" \
    --local-suites "$REPO_ROOT/conformance/local-suites.yaml" \
    --out "$OUT" \
    --upstream-sha "$UPSTREAM_SHA" \
    --protocol "$PROTOCOL_VERSION")

echo ""
echo "=== CONFORMANCE.md refreshed ==="
echo "  upstream-conformance@$UPSTREAM_SHA"
echo "  protocol $PROTOCOL_VERSION"
echo "  output:  $OUT"
