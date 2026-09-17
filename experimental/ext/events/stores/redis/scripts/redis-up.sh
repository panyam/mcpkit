#!/usr/bin/env bash
# Redis for the store's integration tests. Idempotent: starts, restarts a
# stopped container, or says it is already up.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

# Defaults mirror the runner's variables; the runner passes them.
NAME="${REDIS_CONTAINER_NAME:-mcpkit-events-redis-test}"
PORT="${REDIS_PORT:-6389}"
IMAGE="${REDIS_IMAGE:-redis:7-alpine}"

if [ -n "$(docker ps -q -f "name=^$NAME$")" ]; then
    echo "Redis already running on localhost:$PORT"
    exit 0
fi

if [ -n "$(docker ps -aq -f "name=^$NAME$")" ]; then
    docker start "$NAME" >/dev/null
else
    docker run -d --name "$NAME" -p "$PORT:6379" "$IMAGE" >/dev/null
fi

echo "Waiting for Redis to be ready..."
for _ in $(seq 1 10); do
    if docker exec "$NAME" redis-cli ping 2>/dev/null | grep -q PONG; then
        echo "Redis ready on localhost:$PORT"
        exit 0
    fi
    sleep 0.5
done
