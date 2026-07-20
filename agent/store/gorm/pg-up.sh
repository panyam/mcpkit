#!/usr/bin/env bash
# Ensure the agent/store/gorm test Postgres container is running.
#
# Shared by the Makefile + justfile `testpg` (mode: wait) and `updb`
# (mode: default) recipes.
#
# Usage: pg-up.sh [wait]
#   wait  testpg mode — silent-start the container if missing, then poll
#         pg_isready until it accepts connections (no output if already up).
#   (none) updb mode — print "already running" if up, else start it (no wait).
#
# Env (required, supplied by the caller's PG_* variables):
#   PG_CONTAINER_NAME PG_PORT PG_USER PG_PASSWORD PG_DB PG_IMAGE
set -e

MODE="${1:-}"

if [ "$MODE" = "wait" ]; then
    if ! docker ps --format '{{.Names}}' | grep -q "^${PG_CONTAINER_NAME}$"; then
        echo "Starting Postgres container ${PG_CONTAINER_NAME} on port ${PG_PORT}..."
        docker run --rm -d \
            --name "${PG_CONTAINER_NAME}" \
            -e POSTGRES_USER="${PG_USER}" \
            -e POSTGRES_PASSWORD="${PG_PASSWORD}" \
            -e POSTGRES_DB="${PG_DB}" \
            -p "${PG_PORT}:5432" \
            "${PG_IMAGE}" >/dev/null
        echo "Waiting for Postgres to accept connections..."
        for i in 1 2 3 4 5 6 7 8 9 10; do
            if docker exec "${PG_CONTAINER_NAME}" pg_isready -U "${PG_USER}" -d "${PG_DB}" >/dev/null 2>&1; then
                break
            fi
            sleep 1
        done
    fi
else
    if docker ps --format '{{.Names}}' | grep -q "^${PG_CONTAINER_NAME}$"; then
        echo "Postgres container ${PG_CONTAINER_NAME} already running."
    else
        docker run --rm -d \
            --name "${PG_CONTAINER_NAME}" \
            -e POSTGRES_USER="${PG_USER}" \
            -e POSTGRES_PASSWORD="${PG_PASSWORD}" \
            -e POSTGRES_DB="${PG_DB}" \
            -p "${PG_PORT}:5432" \
            "${PG_IMAGE}"
    fi
fi
