#!/usr/bin/env bash
# The exercise for mission #1701: an agent connects to a server with 200 tools
# and some skills, and sees groups instead of a flat list.
#
#   make exercise-disclosure
#
# Three steps, one table:
#   1. testconf-skills, the SEP-2640 server suite against examples/skills.
#   2. The sep-2640-client-* scenarios, each driving cmd/testclient. The names
#      come from conformance/client-check-counts.json.
#   3. examples/disclosure on each wire, checked by its own --check mode. It
#      connects a plain client and a groups client, and prints the size of
#      groups/get next to the flat tools/list.
#
# Step 3's groups rows fail until mcpkit serves the groups extension (#1685).
# Exits 1 if any step failed.
set -u
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LOG=$(mktemp -d -t exercise-disclosure.XXXXXX)
fail=0

REPO_ROOT="$ROOT" . "$ROOT/conformance/path-defaults.sh"

echo "=== exercise-disclosure @ $(git -C "$ROOT" rev-parse --short HEAD) ==="

echo "  1. skills server suite:"
make -C "$ROOT" testconf-skills >"$LOG/skills.log" 2>&1 || fail=1
echo "    $(grep -E '^testconf-skills: ' "$LOG/skills.log" | tail -1 | sed 's/ (artifacts.*//' || true)"
grep -q '^testconf-skills: ' "$LOG/skills.log" || echo "    testconf-skills: no summary, see $LOG/skills.log"

echo "  2. skills client scenarios:"
if [ ! -d "$MCPCONFORMANCE_CLIENT_PATH/dist" ]; then
    echo "    FAIL no built conformance checkout at $MCPCONFORMANCE_CLIENT_PATH (run make testconf-client once)"
    fail=1
elif ! (cd "$ROOT/cmd/testclient" && go build -buildvcs=false -o "$LOG/testclient" .) >"$LOG/testclient-build.log" 2>&1; then
    echo "    FAIL cmd/testclient did not build, see $LOG/testclient-build.log"
    fail=1
else
    # Each line is a scenario name and the check count the snapshot pins. A
    # passing scenario with fewer checks hides a setup failure.
    python3 -c 'import json,sys; [print(k, v) for k, v in json.load(open(sys.argv[1])).items() if k.startswith("sep-2640-client-")]' \
        "$ROOT/conformance/client-check-counts.json" >"$LOG/scenarios.txt"
    while read -r s want; do
        out="$LOG/client-$s"
        (cd "$MCPCONFORMANCE_CLIENT_PATH" && node dist/index.js client --command "$LOG/testclient" --scenario "$s" -o "$out") \
            </dev/null >"$out.log" 2>&1
        rc=$?
        fails=$(cat $(find "$out" -name checks.json 2>/dev/null) /dev/null | grep -c '"status": "FAILURE"')
        passes=$(cat $(find "$out" -name checks.json 2>/dev/null) /dev/null | grep -c '"status": "SUCCESS"')
        if [ "$rc" -eq 0 ] && [ "$fails" -eq 0 ] && [ "$passes" -ge "$want" ]; then
            printf '    PASS %-38s %d of %d checks\n' "$s" "$passes" "$want"
        else
            printf '    FAIL %-38s %d pass (want %d) / %d fail, exit %d, see %s.log\n' "$s" "$passes" "$want" "$fails" "$rc" "$out"
            fail=1
        fi
    done <"$LOG/scenarios.txt"
fi

echo "  3. examples/disclosure, plain and groups clients:"
BIN="$LOG/disclosure"
if ! (cd "$ROOT/examples/disclosure" && go build -o "$BIN" .) >"$LOG/disclosure-build.log" 2>&1; then
    echo "    FAIL examples/disclosure did not build, see $LOG/disclosure-build.log"
    echo "Full logs in $LOG"
    exit 1
fi
for wire in legacy stateless; do
    port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')
    "$BIN" --serve --addr="127.0.0.1:$port" --wire="$wire" >"$LOG/serve-$wire.log" 2>&1 &
    pid=$!
    for _ in $(seq 1 50); do
        (exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null && break
        sleep 0.1
    done
    "$BIN" --check --url "http://127.0.0.1:$port" --wire="$wire" | sed 's/^/  /'
    [ "${PIPESTATUS[0]}" -ne 0 ] && fail=1
    kill "$pid" 2>/dev/null
    wait "$pid" 2>/dev/null
done

echo "Full logs in $LOG"
exit "$fail"
