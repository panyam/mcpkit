#!/usr/bin/env bash
# Start the Keycloak container for auth interop tests (skips if already healthy).
#
# Shared by the root Makefile + justfile `upkcl` recipes and by
# scripts/testkcl-auto.sh. Runner-agnostic: the KC_* constants self-default
# here, so a direct call (e.g. from testall.sh) needs no env; the recipes may
# still override any of them via env.
set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
: "${KC_IMAGE:=quay.io/keycloak/keycloak:26.0}"
: "${KC_PORT:=8180}"
: "${KC_CONTAINER:=mcpkit-keycloak}"
: "${KC_REALM:=mcpkit-test}"
KC_REALM_URL="http://localhost:${KC_PORT}/realms/${KC_REALM}"

if curl -sf "$KC_REALM_URL" > /dev/null 2>&1; then
    echo "Keycloak already running on port ${KC_PORT} — skipping start"
else
    docker rm -f "${KC_CONTAINER}" 2>/dev/null || true
    docker run -d --name "${KC_CONTAINER}" \
        -p "${KC_PORT}:8080" \
        -e KC_BOOTSTRAP_ADMIN_USERNAME=admin \
        -e KC_BOOTSTRAP_ADMIN_PASSWORD=admin \
        -v "${ROOT}/tests/keycloak/realm.json:/opt/keycloak/data/import/realm.json" \
        "${KC_IMAGE}" start-dev --import-realm \
        --log-level=INFO,org.keycloak.events:DEBUG
    echo "Keycloak starting on port ${KC_PORT}... (realm import takes ~30s)"
    echo "Waiting for realm import to land before flipping master sslRequired..."
    for i in $(seq 1 60); do
        curl -sf "$KC_REALM_URL" > /dev/null 2>&1 && break
        sleep 1
    done
    echo "Flipping master realm sslRequired=NONE so the test admin-token grant works over HTTP..."
    docker exec "${KC_CONTAINER}" /opt/keycloak/bin/kcadm.sh config credentials \
        --server http://localhost:8080 --realm master --user admin --password admin >/dev/null 2>&1 && \
    docker exec "${KC_CONTAINER}" /opt/keycloak/bin/kcadm.sh update realms/master -s sslRequired=NONE >/dev/null && \
    echo "[upkcl] master sslRequired=NONE (the bcl_test admin-cli password grant requires it)"
    echo "Run 'kcllogs' to watch startup, 'testkcl' when ready"
fi
