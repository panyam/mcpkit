#!/usr/bin/env bash
# Signs out every USER session in all three tenant realms. The
# mcp-event-server client credentials are untouched, so the replicas keep
# introspecting while operator tokens go stale.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
. scripts/common.sh

REALMS="${REALMS:-asgard babylon camelot}"

compose exec -T keycloak /opt/keycloak/bin/kcadm.sh config credentials \
    --server http://keycloak:8080 --realm master --user admin --password admin >/dev/null

for realm in $REALMS; do
    compose exec -T keycloak /opt/keycloak/bin/kcadm.sh create logout-all -r "$realm" >/dev/null
    echo "[clear_all_tokens] $realm: all user sessions signed out"
done
