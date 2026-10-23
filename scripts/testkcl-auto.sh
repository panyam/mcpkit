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

# The suite reads KEYCLOAK_URL, so a non-default KC_PORT has to reach it. And
# since this target's whole job is to make Keycloak be there, an unreachable one
# fails the run rather than passing it as 19 skips (#1597).
export KEYCLOAK_URL="http://localhost:${KC_PORT}"
export KEYCLOAK_REQUIRED="${KEYCLOAK_REQUIRED:-1}"

if ! curl -sf --max-time 3 "$KC_REALM_URL" > /dev/null 2>&1; then
    # Something holds the port but serves no mcpkit-test realm, e.g. another
    # session's Keycloak. Starting ours would fail to bind and leave the tests
    # pointed at the wrong server.
    if (exec 3<>"/dev/tcp/127.0.0.1/${KC_PORT}") 2>/dev/null; then
        echo "REFUSING: port ${KC_PORT} is in use but serves no ${KC_REALM} realm." >&2
        echo "  Free it, or run on another port: KC_PORT=8181 make testkcl-auto" >&2
        exit 1
    fi
    echo "Starting Keycloak for interop tests..."
    bash "$ROOT/scripts/keycloak-up.sh"
    echo "Waiting for Keycloak realm..."
    for i in $(seq 1 60); do
        curl -sf --max-time 3 "$KC_REALM_URL" > /dev/null 2>&1 && break
        sleep 2
    done
    KC_STARTED=1
fi
(cd "$ROOT/tests/keycloak" && go test ./... -count=1 -timeout 120s -v)
EXIT=$?
if [ "${KC_STARTED:-}" = "1" ]; then docker rm -f "${KC_CONTAINER}" 2>/dev/null || true; fi
exit $EXIT
