#!/usr/bin/env bash
# Grade another implementation's events server against the SEP-3415 server
# scenarios, through an OAuth login. Informational, like testconf-events.
#
#   make conf-events-remote                                   # metronome
#   EVENTS_REMOTE_URL=https://other.example/mcp make conf-events-remote
#   EVENTS_REMOTE_SCENARIOS=events-discovery make conf-events-remote
#
# The conformance runner cannot send a bearer token, so this starts
# ext/auth/cmd/mcp-auth-proxy on loopback and points the runner at it. The
# proxy registers a client, logs in headlessly (approving a demo consent page
# if the server shows one), and logs in again on any 401. See the proxy's
# package comment for what "headlessly" covers.
#
# events-webhook-delivery is left out: it needs the server to reach the
# harness's loopback receiver, which a remote server cannot, and it drives an
# mcpkit-only control tool to allow that origin.
set -u
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_common.sh"

URL="${EVENTS_REMOTE_URL:-https://metronome-mcp.fly.dev/mcp}"
PORT="${EVENTS_REMOTE_PORT:-18201}"
SCENARIOS="${EVENTS_REMOTE_SCENARIOS:-events-discovery events-poll events-push events-webhook}"

require_conf_dir MCPCONFORMANCE_EVENTS_PATH \
    "Run 'make conf-events-setup' to create it, or set MCPCONFORMANCE_EVENTS_PATH=<path-to-clone>."
build_conf_dist MCPCONFORMANCE_EVENTS_PATH

OUT=$(mktemp -d -t conf-events-remote.XXXXXX)
(cd "${REPO_ROOT}/ext/auth" && go build -o "$OUT/mcp-auth-proxy" ./cmd/mcp-auth-proxy) || exit 1

require_port_free "$PORT"
echo "Proxying 127.0.0.1:${PORT} -> ${URL}, scratch dir $OUT"
"$OUT/mcp-auth-proxy" -server "$URL" -addr "127.0.0.1:${PORT}" > "$OUT/proxy.log" 2>&1 &
PID=$!
trap 'kill $PID 2>/dev/null' EXIT
for _ in $(seq 1 30); do
    grep -q 'proxying ' "$OUT/proxy.log" 2>/dev/null && break
    kill -0 "$PID" 2>/dev/null || break
    sleep 0.5
done
require_fixture_alive "$PID" "$OUT/proxy.log"

for S in ${SCENARIOS}; do
    TIMEOUT_ARGS=""
    [ "${S}" = "events-push" ] && TIMEOUT_ARGS="--timeout 60000"
    # shellcheck disable=SC2086 # deliberate word-splitting: empty means no flag
    (cd "${MCPCONFORMANCE_EVENTS_PATH}" && \
        node dist/index.js server --url "http://127.0.0.1:${PORT}/mcp" --scenario "${S}" \
            ${TIMEOUT_ARGS} -o "$OUT/checks-${S}" > "$OUT/runner-${S}.log" 2>&1)
    SRC=$?
    SUMMARY=$(grep -E "Passed:" "$OUT/runner-${S}.log" | tail -1 | sed 's/\x1b\[[0-9;]*m//g')
    echo "  ${S}: ${SUMMARY:-runner exited ${SRC} (see $OUT/runner-${S}.log)}"
done

# A re-login mid-run means the server forgot its clients, usually a restart.
RELOGINS=$(grep -c '401 from' "$OUT/proxy.log")
[ "$RELOGINS" -gt 0 ] && echo "  note: the proxy logged in again ${RELOGINS} time(s); see $OUT/proxy.log"
echo "Full logs in $OUT"
exit 0
