#!/usr/bin/env bash
# Start Keycloak if needed, run the interop tests, stop the container after
# (only if this script started it).
#
# Shared by the root Makefile + justfile `testkcl-auto` recipes and dispatched
# directly by scripts/testall.sh. Calls scripts/keycloak-up.sh to bring up the
# container (script-calls-script). Runner-agnostic: the KC_* constants
# self-default here so a direct call needs no env; recipes may override via env.
set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
: "${KC_PORT:=8180}"
: "${KC_REALM:=mcpkit-test}"
: "${KC_CONTAINER:=mcpkit-keycloak}"
KC_REALM_URL="http://localhost:${KC_PORT}/realms/${KC_REALM}"

if ! curl -sf "$KC_REALM_URL" > /dev/null 2>&1; then
    echo "Starting Keycloak for interop tests..."
    bash "$ROOT/scripts/keycloak-up.sh"
    echo "Waiting for Keycloak realm..."
    for i in $(seq 1 60); do
        curl -sf "$KC_REALM_URL" > /dev/null 2>&1 && break
        sleep 2
    done
    KC_STARTED=1
fi
(cd "$ROOT/tests/keycloak" && go test ./... -count=1 -timeout 120s -v)
EXIT=$?
if [ "${KC_STARTED:-}" = "1" ]; then docker rm -f "${KC_CONTAINER}" 2>/dev/null || true; fi
exit $EXIT
