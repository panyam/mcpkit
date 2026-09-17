#!/usr/bin/env bash
# Postgres for the GORM store's integration tests.
#
# Usage: pg.sh <up|test>
#   up    start the container if it is not already running
#   test  ensure it is up and accepting connections, then run the suite
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

# Defaults mirror the runner's variables. The runner passes them, so these only
# apply when the script is run by hand; they must not drift from the Makefile.
NAME="${PG_CONTAINER_NAME:-mcpkit-events-gorm-test-pg}"
PORT="${PG_PORT:-5434}"
USER="${PG_USER:-postgres}"
PASSWORD="${PG_PASSWORD:-testpassword}"
DB="${PG_DB:-mcpkit_events_test}"
IMAGE="${PG_IMAGE:-postgres:15-alpine}"

running() {
    docker ps --format '{{.Names}}' | grep -q "^$NAME$"
}

start() {
    docker run --rm -d \
        --name "$NAME" \
        -e POSTGRES_USER="$USER" \
        -e POSTGRES_PASSWORD="$PASSWORD" \
        -e POSTGRES_DB="$DB" \
        -p "$PORT:5432" \
        "$IMAGE" "$@"
}

wait_ready() {
    echo "Waiting for Postgres to accept connections..."
    for _ in $(seq 1 10); do
        docker exec "$NAME" pg_isready -U "$USER" -d "$DB" >/dev/null 2>&1 && return 0
        sleep 1
    done
}

case "${1:?usage: pg.sh <up|test>}" in
    up)
        if running; then
            echo "Postgres container $NAME already running."
        else
            echo "Starting Postgres container $NAME on port $PORT..."
            start
        fi
        ;;
    test)
        if ! running; then
            echo "Starting Postgres container $NAME on port $PORT..."
            start >/dev/null
            wait_ready
        fi
        MCPKIT_EVENTS_TEST_PGHOST=localhost \
        MCPKIT_EVENTS_TEST_PGPORT="$PORT" \
        MCPKIT_EVENTS_TEST_PGUSER="$USER" \
        MCPKIT_EVENTS_TEST_PGPASSWORD="$PASSWORD" \
        MCPKIT_EVENTS_TEST_PGDB="$DB" \
        go test ./... -count=1 -timeout 120s
        ;;
    *) echo "usage: pg.sh <up|test>" >&2; exit 2 ;;
esac
