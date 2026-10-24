#!/usr/bin/env bash
# The exercise for mission #1552: a 2026-07-28 client gets spec-correct
# results from mcpkit on both wires.
#
#   make exercise-wire                 # parity rows, then the three suites
#   EXERCISE_WIRE_ROWS_ONLY=1 make exercise-wire   # parity rows only
#
# The parity rows are TestWireParity in server/ and in experimental/ext/agents.
# Each sends the same requests over the session wire and the SEP-2575
# stateless wire and checks one thing both must agree on; a row's name leads
# with the ticket it guards. The suites (testconf, testconf-stateless,
# testconf-agents) were the exercise before #1555, and stay in it because a
# row only covers what it was written for.
#
# Exits 1 if any parity row or suite failed, so a red exercise is visible in
# the exit code as well as the table.
set -u
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LOG=$(mktemp -d -t exercise-wire.XXXXXX)
fail=0

rows() {
    # $1 = label, $2 = module dir, $3 = package
    (cd "$ROOT/$2" && go test "$3" -run '^TestWireParity$' -count=1 -v) >"$LOG/$1.log" 2>&1
    local rc=$?
    if grep -q '\[build failed\]\|^# ' "$LOG/$1.log"; then
        echo "  $1: BUILD FAILED, see $LOG/$1.log"
        fail=1
        return
    fi
    grep -E '^    --- (PASS|FAIL): TestWireParity/' "$LOG/$1.log" |
        sed -E 's/^    --- (PASS|FAIL): TestWireParity\/([0-9]+)_(.*) \(.*/\1 #\2 \3/; s/^    --- (PASS|FAIL): TestWireParity\/(.*) \(.*/\1 - \2/' |
        while read -r verdict ticket name; do
            printf '  %-4s %-6s %-9s %s\n' "$verdict" "$ticket" "$1" "${name//_/ }"
        done
    [ "$rc" -ne 0 ] && fail=1
}

echo "=== exercise-wire @ $(git -C "$ROOT" rev-parse --short HEAD) ==="
echo "  parity rows (session vs stateless, 2026-07-28):"
rows server . ./server
rows agents experimental/ext/agents .

if [ "${EXERCISE_WIRE_ROWS_ONLY:-}" = "1" ]; then
    echo "Full logs in $LOG"
    exit "$fail"
fi

echo "  suites:"
make -C "$ROOT" testconf >"$LOG/testconf.log" 2>&1 || fail=1
echo "    testconf: $(grep -E '^Total: ' "$LOG/testconf.log" | tail -1 || echo 'no total, see log')"
make -C "$ROOT" testconf-stateless >"$LOG/stateless.log" 2>&1 || fail=1
echo "    $(grep -E '^testconf-stateless: ' "$LOG/stateless.log" | tail -1 | sed 's/ (artifact.*//' || echo 'testconf-stateless: no summary, see log')"
make -C "$ROOT" testconf-agents >"$LOG/agents-suite.log" 2>&1 || fail=1
grep -E '^  sep-[0-9]+-agents-[a-z-]+: Passed' "$LOG/agents-suite.log" | sed 's/^/  /'
# testconf-agents is INFO and exits 0 whatever it grades, so read its numbers.
grep -E '^  sep-[0-9]+-agents-[a-z-]+: Passed' "$LOG/agents-suite.log" | grep -qv ' 0 failed' && fail=1

echo "Full logs in $LOG"
exit "$fail"
