#!/usr/bin/env bash
# The exercise for mission #1508: grade mcpkit's events server and Go client
# against the SEP-3415 conformance suites, and print one table.
#
#   make conf-events-setup   # once, creates the two suite checkouts
#   make exercise-events
#
# Runs testconf-events (five server scenarios against
# examples/events/kitchen-sink) and testconf-events-client (four client
# scenarios against the Go driver), then prints every scenario's summary line
# together. Exits 0 like both suites, which are INFO: the table is the result,
# and a red row is a finding to log on #1508 rather than a broken build.
set -u
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LOG=$(mktemp -d -t exercise-events.XXXXXX)

echo "=== server scenarios (testconf-events) ==="
bash "$ROOT/conformance/scripts/conf-events.sh" 2>&1 | tee "$LOG/server.log"
SERVER_RC=${PIPESTATUS[0]}
echo ""
echo "=== client scenarios (testconf-events-client) ==="
bash "$ROOT/conformance/scripts/conf-events-client.sh" 2>&1 | tee "$LOG/client.log"
CLIENT_RC=${PIPESTATUS[0]}

echo ""
echo "=== exercise-events @ $(git -C "$ROOT" rev-parse --short HEAD) ==="
grep -hE '^  events-[a-z-]+( \([0-9-]+\))?: ' "$LOG/server.log" "$LOG/client.log"
# Both suites exit 0 whatever they grade, so a non-zero exit means one refused
# to grade at all (a held port, a fixture that died; see _common.sh).
[ "$SERVER_RC" -ne 0 ] && echo "  server suite REFUSED to grade (exit $SERVER_RC), see above"
[ "$CLIENT_RC" -ne 0 ] && echo "  client suite REFUSED to grade (exit $CLIENT_RC), see above"

# A suite checkout behind upstream grades against old scenarios, and its
# warning scrolled past at the top. Repeat it here, beside the numbers.
grep -h "commit(s) behind @{u}" "$LOG/server.log" "$LOG/client.log" | sed 's/^ *//'

# The suite carried a sep-9999 placeholder until #1502. Seeing it again means a
# checkout predates the rename.
if grep -q 'sep-9999-' "$LOG/server.log" "$LOG/client.log"; then
    echo "WARNING: sep-9999- check ids in the output. A suite checkout predates the SEP-3415 rename; pull it."
fi
echo "Full logs in $LOG"
exit 0
