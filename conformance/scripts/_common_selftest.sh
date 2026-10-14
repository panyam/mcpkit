#!/usr/bin/env bash
# Self-test for the fixture guards in _common.sh (require_port_free,
# require_fixture_alive). Each case runs the helper in a subshell, because
# both exit the calling script when they refuse.
#
# Uses python3 for a throwaway listener, since nc's flags differ between the
# BSD and GNU builds the runners carry.
set -u
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_common.sh"

FAILS=0
check() {
    local name="$1" want="$2" got="$3"
    if [ "$want" = "$got" ]; then
        echo "  ok    ${name}"
    else
        echo "  FAIL  ${name}: want ${want}, got ${got}"
        FAILS=$((FAILS + 1))
    fi
}

PORT=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')
TMP=$(mktemp -d -t conf-common-selftest.XXXXXX)

(require_port_free "$PORT") >/dev/null 2>&1
check "require_port_free passes on a free port" 0 $?

python3 -c "
import socket, time
s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(('127.0.0.1', $PORT)); s.listen(1); time.sleep(30)" &
LISTENER=$!
for _ in 1 2 3 4 5 6 7 8 9 10; do
    (exec 3<>"/dev/tcp/127.0.0.1/${PORT}") 2>/dev/null && break
    sleep 0.2
done
(require_port_free "$PORT") >"$TMP/busy.out" 2>&1
check "require_port_free refuses a port something holds" 1 $?
grep -q "port ${PORT} is already in use" "$TMP/busy.out"
check "the refusal names the port" 0 $?
kill "$LISTENER" 2>/dev/null; wait "$LISTENER" 2>/dev/null

sleep 30 &
ALIVE=$!
(require_fixture_alive "$ALIVE") >/dev/null 2>&1
check "require_fixture_alive passes on a running fixture" 0 $?
kill "$ALIVE" 2>/dev/null; wait "$ALIVE" 2>/dev/null

echo "listen tcp :${PORT}: bind: address already in use" > "$TMP/fixture.log"
true &
DEAD=$!
wait "$DEAD" 2>/dev/null
(require_fixture_alive "$DEAD" "$TMP/fixture.log") >"$TMP/dead.out" 2>&1
check "require_fixture_alive refuses a fixture that exited" 1 $?
grep -q "bind: address already in use" "$TMP/dead.out"
check "the refusal shows the fixture log's tail" 0 $?

rm -rf "$TMP"
if [ "$FAILS" -ne 0 ]; then
    echo "conf-common selftest: ${FAILS} case(s) failed"
    exit 1
fi
echo "conf-common selftest: 6/6 cases pass"
