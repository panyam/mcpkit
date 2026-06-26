#!/bin/bash
# Audit harness: runs modelcontextprotocol/conformance@main against mcpkit and
# emits a per-SEP report at conformance/UPSTREAM_AUDIT.md.
#
# Informational — exits 0 regardless of how many scenarios fail. The point is
# the report, not a pass/fail gate. See conformance/Makefile testconf-upstream-audit.
#
# Required:
#   MCPCONFORMANCE_BASE_PATH — path to a clone of modelcontextprotocol/conformance
#                              (the Makefile target fail-fasts if missing).
#
# Optional:
#   AUDIT_OUT     — scratch dir for raw JSON results (default: /tmp/conf-audit)
#   AUDIT_PORT    — port for testserver (default: 18099)
#   AUDIT_VERBOSE — set to 1 to stream stdout/stderr instead of redirecting

set -u  # NOT -e — keep running past individual scenario failures

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CONF_DIR="${MCPCONFORMANCE_BASE_PATH:?MCPCONFORMANCE_BASE_PATH must be set}"
AUDIT_OUT="${AUDIT_OUT:-/tmp/conf-audit}"
AUDIT_PORT="${AUDIT_PORT:-18099}"
# SEP-2663 tasks scenarios are graded against examples/tasks-v2 (its own module
# with ext/tasks wired in), NOT cmd/testserver — keeping the root module free of
# the ext/tasks dependency. Same split as testconf-stateless / examples/stateless.
AUDIT_TASKS_PORT="${AUDIT_TASKS_PORT:-18101}"

SERVER_PID=""
TASKS_PID=""
cleanup() {
    if [ -n "$SERVER_PID" ]; then
        kill "$SERVER_PID" 2>/dev/null || true
        wait "$SERVER_PID" 2>/dev/null || true
    fi
    if [ -n "$TASKS_PID" ]; then
        kill "$TASKS_PID" 2>/dev/null || true
        wait "$TASKS_PID" 2>/dev/null || true
    fi
}
trap cleanup EXIT

# Kill stale process on audit port
if lsof -i ":$AUDIT_PORT" -t >/dev/null 2>&1; then
    echo "Killing stale process on port $AUDIT_PORT..."
    lsof -i ":$AUDIT_PORT" -t | xargs kill 2>/dev/null || true
    sleep 1
fi

rm -rf "$AUDIT_OUT"
mkdir -p "$AUDIT_OUT/server" "$AUDIT_OUT/client-auth"

# --- 1. Build mcpkit fixtures ------------------------------------------------

echo "=== Building cmd/testserver and cmd/testclient ==="
(cd "$REPO_ROOT" && go build -o bin/testserver ./cmd/testserver)
(cd "$REPO_ROOT/cmd/testclient" && go build -buildvcs=false -o "$REPO_ROOT/bin/testclient" .)

# --- 2. Install / build upstream conformance --------------------------------

echo "=== Installing upstream conformance deps (builds dist/ via prepare) ==="
(cd "$CONF_DIR" && npm install --silent)

# --- 3. Spawn testserver ----------------------------------------------------

echo "=== Spawning cmd/testserver on :$AUDIT_PORT (Streamable HTTP) ==="
STREAMABLE=1 PORT="$AUDIT_PORT" "$REPO_ROOT/bin/testserver" \
    > "$AUDIT_OUT/testserver.log" 2>&1 &
SERVER_PID=$!

# Wait for readiness via initialize handshake
echo -n "Waiting for testserver..."
for i in $(seq 1 30); do
    if curl -s -o /dev/null -w "%{http_code}" -X POST "http://localhost:$AUDIT_PORT/mcp" \
        -H "Content-Type: application/json" \
        -H "Accept: application/json, text/event-stream" \
        -d '{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"audit","version":"0.0"}}}' \
        2>/dev/null | grep -q "200"; then
        echo " ready"
        break
    fi
    echo -n "."
    sleep 1
done

if ! kill -0 "$SERVER_PID" 2>/dev/null; then
    echo " FAIL: testserver exited during startup"
    cat "$AUDIT_OUT/testserver.log"
    exit 1
fi

# --- 4. Run upstream server suite (suite=all → includes pending/draft) ------

echo ""
echo "=== Running upstream server suite ==="
SERVER_REDIRECT="$AUDIT_OUT/server/run.log"
if [ "${AUDIT_VERBOSE:-}" = "1" ]; then
    SERVER_REDIRECT=/dev/stdout
fi
(cd "$CONF_DIR" && node dist/index.js server \
    --url "http://localhost:$AUDIT_PORT/mcp" \
    --suite all \
    -o "$AUDIT_OUT/server" \
    > "$SERVER_REDIRECT" 2>&1) || true

# --- 4b. Re-grade SEP-2663 tasks scenarios against examples/tasks-v2 ---------
#
# cmd/testserver is intentionally minimal and does NOT implement the tasks
# extension (that would pull ext/tasks into the root module). The bulk `--suite
# all` sweep above therefore fails the tasks-* scenarios with "unknown tool".
# Those throwaway results are discarded here and the scenarios are re-graded
# against examples/tasks-v2 — a standalone module that wires ext/tasks — on a
# second port. Mirrors how testconf-stateless grades examples/stateless. The
# committed verdict for tasks scenarios comes entirely from examples/tasks-v2.
echo ""
echo "=== Re-grading SEP-2663 tasks scenarios against examples/tasks-v2 ==="
rm -rf "$AUDIT_OUT"/server/server-tasks-*
(cd "$REPO_ROOT/examples/tasks-v2" && go build -o tasks-v2 .)
STREAMABLE=1 "$REPO_ROOT/examples/tasks-v2/tasks-v2" --serve --addr ":$AUDIT_TASKS_PORT" \
    > "$AUDIT_OUT/tasks-v2.log" 2>&1 &
TASKS_PID=$!
echo -n "Waiting for tasks-v2..."
for i in $(seq 1 30); do
    if curl -s -o /dev/null -w "%{http_code}" -X POST "http://localhost:$AUDIT_TASKS_PORT/mcp" \
        -H "Content-Type: application/json" \
        -H "Accept: application/json, text/event-stream" \
        -d '{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"audit","version":"0.0"}}}' \
        2>/dev/null | grep -q "200"; then
        echo " ready"
        break
    fi
    echo -n "."
    sleep 1
done
TASKS_SCENARIOS=$(cd "$CONF_DIR" && node dist/index.js list --server 2>/dev/null \
    | awk '/^  - /{print $2}' | grep '^tasks-')
for scenario in $TASKS_SCENARIOS; do
    (cd "$CONF_DIR" && node dist/index.js server \
        --url "http://localhost:$AUDIT_TASKS_PORT/mcp" \
        --scenario "$scenario" \
        --suite all \
        -o "$AUDIT_OUT/server" \
        >> "$AUDIT_OUT/server/tasks-run.log" 2>&1) || true
done
kill "$TASKS_PID" 2>/dev/null || true
wait "$TASKS_PID" 2>/dev/null || true
TASKS_PID=""
echo "Re-graded $(echo "$TASKS_SCENARIOS" | wc -l | tr -d ' ') tasks scenarios against examples/tasks-v2"

# --- 5. Run upstream client scenarios (sequential — parallel mode is flaky) -

# `client --suite all` runs in parallel and can crash mid-run with
# "SyntaxError: Unexpected end of JSON input" when one child's output races
# the runner's IncomingMessage parser. We iterate sequentially via
# `--scenario <name>` to get a deterministic per-scenario verdict.
echo ""
echo "=== Running upstream client scenarios sequentially (driver: $REPO_ROOT/bin/testclient) ==="
CLIENT_LOG="$AUDIT_OUT/client-auth/run.log"
> "$CLIENT_LOG"
CLIENT_SCENARIOS=$(cd "$CONF_DIR" && node dist/index.js list --client 2>/dev/null \
    | awk '/^  - /{print $2}')
TOTAL=$(echo "$CLIENT_SCENARIOS" | wc -l | tr -d ' ')
N=0
for scenario in $CLIENT_SCENARIOS; do
    N=$((N+1))
    if [ "${AUDIT_VERBOSE:-}" = "1" ]; then
        echo "[$N/$TOTAL] $scenario"
    fi
    (cd "$CONF_DIR" && node dist/index.js client \
        --command "$REPO_ROOT/bin/testclient" \
        --scenario "$scenario" \
        --timeout 15000 \
        -o "$AUDIT_OUT/client-auth" \
        >> "$CLIENT_LOG" 2>&1) || true
done
echo "Ran $N/$TOTAL client scenarios sequentially → $AUDIT_OUT/client-auth/"

# --- 6. Dump upstream scenario inventory (for harness-gap categorization) ---

echo ""
echo "=== Capturing upstream scenario inventory ==="
(cd "$CONF_DIR" && node dist/index.js list --server > "$AUDIT_OUT/scenarios-server.txt") || true
(cd "$CONF_DIR" && node dist/index.js list --client > "$AUDIT_OUT/scenarios-client.txt") || true

# --- 7. Generate the report -------------------------------------------------

echo ""
echo "=== Generating conformance/UPSTREAM_AUDIT.md ==="
(cd "$CONF_DIR" && npx tsx "$REPO_ROOT/scripts/conformance-audit-report.ts" \
    "$AUDIT_OUT" \
    "$REPO_ROOT/conformance/UPSTREAM_AUDIT.md") || {
    echo "WARN: report generator failed; raw results in $AUDIT_OUT"
    exit 0
}

echo ""
echo "=== Audit complete ==="
echo "Report:      $REPO_ROOT/conformance/UPSTREAM_AUDIT.md"
echo "Raw results: $AUDIT_OUT"
exit 0
