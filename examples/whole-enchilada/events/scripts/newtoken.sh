#!/usr/bin/env bash
# Acquires a bearer token from the tenant's Keycloak realm.
#
# Usage: newtoken.sh <browser|password>
#   browser   interactive; opens that realm's login page
#   password  ROPC, for CI and scripting; needs USER and PASSWORD
#
# Prints ONLY the access token, so `$(make newtoken TENANT=A)` captures cleanly.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
. scripts/common.sh

GRANT="${1:?internal: newtoken.sh needs browser|password}"

TENANT="${TENANT:-}"
USER="${USER:-}"
PASSWORD="${PASSWORD:-}"
ONEAUTH="${ONEAUTH:-oneauth}"
KEYCLOAK_PORT="${KEYCLOAK_PORT:-8180}"
KEYCLOAK_HOST="${KEYCLOAK_HOST:-http://localhost:$KEYCLOAK_PORT}"
DEMO_CLIENT_ID="${DEMO_CLIENT_ID:-mcp-events-poller}"
DEMO_CLIENT_SECRET="${DEMO_CLIENT_SECRET:-mcpkit-demo-secret-DEMO-ONLY}"

REALM_URL="$KEYCLOAK_HOST/realms/$(tenant_realm "$TENANT")"
COMMON=(--client-id "$DEMO_CLIENT_ID" --client-secret "$DEMO_CLIENT_SECRET" --format=access-token-only)

case "$GRANT" in
    browser)
        if [ -z "$TENANT" ]; then
            usage_for newtoken "TENANT=A|B|C"
        fi
        exec "$ONEAUTH" token browser "$REALM_URL" "${COMMON[@]}"
        ;;
    password)
        if [ -z "$TENANT" ] || [ -z "$USER" ] || [ -z "$PASSWORD" ]; then
            usage_for newtoken-ci "TENANT=A|B|C" "USER=<user>" "PASSWORD=<pw>"
        fi
        "$ONEAUTH" token password "$REALM_URL" --user "$USER" --password "$PASSWORD" "${COMMON[@]}"
        # The recipe ended with a bare echo; a ROPC token has no trailing
        # newline and the prompt landed glued to it.
        echo
        ;;
    *)
        echo "internal: unknown grant '$GRANT'" >&2
        exit 2
        ;;
esac
