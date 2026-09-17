# Makefile for experimental/ext/events/stores/redis
#
# `make test`        — fast path: miniredis (in-process pure-Go Redis, no
#                      Docker required). Default for CI.
# `make testredis`   — boots a Redis container if missing, then runs the
#                      same tests against a real Redis. Exercises wire
#                      compat + reconnect behavior the miniredis path
#                      can't fully verify.
# `make updb`        — start the Redis container (long-running).
# `make downdb`      — stop the Redis container.
# `make dblogs`      — tail container logs.
#
# Redis container settings — non-default port so this doesn't collide
# with any other docker Redis running on 6379.

REDIS_CONTAINER_NAME := mcpkit-events-redis-test
REDIS_PORT           := 6389
REDIS_IMAGE          := redis:7-alpine

# Reaches scripts/ through the environment (C8: recipes dispatch, scripts do the work).
export REDIS_CONTAINER_NAME REDIS_PORT REDIS_IMAGE

.PHONY: test testredis updb downdb dblogs

# Default: miniredis. No Docker required.
test:
	go test -count=1 -timeout 60s ./...

# Real Redis path — same tests, real server. Spins the container if
# absent. Idempotent — re-running while the container is already up
# is a no-op.
testredis: updb
	MCPKIT_EVENTS_TEST_REDIS_ADDR=localhost:$(REDIS_PORT) \
		go test -count=1 -timeout 90s -v ./...

updb:
	@./scripts/redis-up.sh

downdb:
	@docker rm -f $(REDIS_CONTAINER_NAME) 2>/dev/null || true
	@echo "Redis container stopped"

dblogs:
	docker logs -f $(REDIS_CONTAINER_NAME)
