#!/usr/bin/env bash
# Self-test for the Keycloak suite's guards (#1597): testkcl-auto refuses a
# port that something else holds, and KEYCLOAK_REQUIRED turns an unreachable
# Keycloak from 19 skips into a failure. Needs no Docker and no Keycloak.
set -u
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

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

free_port() {
    python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'
}
TMP=$(mktemp -d -t kcl-guards-selftest.XXXXXX)

PORT=$(free_port)
python3 -c "
import socket, time
s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(('127.0.0.1', $PORT)); s.listen(8); time.sleep(60)" &
LISTENER=$!
for _ in $(seq 1 50); do
    (exec 3<>"/dev/tcp/127.0.0.1/${PORT}") 2>/dev/null && break
    sleep 0.1
done
KC_PORT="$PORT" bash "$ROOT/scripts/testkcl-auto.sh" >"$TMP/held.out" 2>&1
check "testkcl-auto refuses a port held by something that is not Keycloak" 1 $?
grep -q "REFUSING: port ${PORT}" "$TMP/held.out"
check "the refusal names the port" 0 $?
kill "$LISTENER" 2>/dev/null; wait "$LISTENER" 2>/dev/null

GONE="http://127.0.0.1:$(free_port)"
(cd "$ROOT/tests/keycloak" && KEYCLOAK_URL="$GONE" go test -count=1 -run TestKeycloak_MCPServer_ValidToken . >"$TMP/skip.out" 2>&1)
check "an unreachable Keycloak skips by default" 0 $?
(cd "$ROOT/tests/keycloak" && KEYCLOAK_REQUIRED=1 KEYCLOAK_URL="$GONE" go test -count=1 -run TestKeycloak_MCPServer_ValidToken . >"$TMP/req.out" 2>&1)
check "an unreachable Keycloak fails with KEYCLOAK_REQUIRED=1" 1 $?

rm -rf "$TMP"
if [ "$FAILS" -ne 0 ]; then
    echo "testkcl guards selftest: ${FAILS} case(s) failed"
    exit 1
fi
echo "testkcl guards selftest: 4/4 cases pass"
