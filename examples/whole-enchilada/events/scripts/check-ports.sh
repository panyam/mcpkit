#!/usr/bin/env bash
# Fails fast naming the fix, rather than letting Docker report a bare
# "port is already allocated" from whichever container got there first.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
. scripts/common.sh

KEYCLOAK_PORT="${KEYCLOAK_PORT:-8180}"

for p in 9090 "$KEYCLOAK_PORT"; do
    if lsof -nP -iTCP:"$p" -sTCP:LISTEN >/dev/null 2>&1; then
        echo "port $p is already in use."
        echo "  usually docker/backends (shared with examples/agents) or a stack you left up."
        echo "  fixes: '$RUNNER stack-down', or 'cd ../../../docker/backends && make down',"
        echo "         or run this stack beside it with '$RUNNER up KEYCLOAK_PORT=8181'."
        exit 1
    fi
done
