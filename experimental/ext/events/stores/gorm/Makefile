# Makefile for experimental/ext/events/stores/gorm
#
# `make test`        — fast path: sqlite (no Docker required). Default for CI.
# `make testpg`      — boots a Postgres container if missing, then runs the
#                      full conformance matrix (inmemory + sqlite + postgres).
# `make updb`        — start the Postgres container (long-running).
# `make downdb`      — stop the Postgres container.
# `make dblogs`      — tail container logs.
#
# Postgres container settings — kept on a non-default port (5434) so this
# project does not collide with any other docker postgres running on 5432
# or 5433.

PG_CONTAINER_NAME := mcpkit-events-gorm-test-pg
PG_PORT           := 5434
PG_USER           := postgres
PG_PASSWORD       := testpassword
PG_DB             := mcpkit_events_test
PG_IMAGE          := postgres:15-alpine

.PHONY: test testsqlite testpg updb downdb dblogs

# Default: sqlite + inmemory only. No Docker required, fast.
test: testsqlite

testsqlite:
	go test ./... -count=1 -timeout 60s

# Run the full conformance matrix including the Postgres backend.
testpg:
	@if ! docker ps --format '{{.Names}}' | grep -q '^$(PG_CONTAINER_NAME)$$'; then \
		echo "Starting Postgres container $(PG_CONTAINER_NAME) on port $(PG_PORT)..."; \
		docker run --rm -d \
			--name $(PG_CONTAINER_NAME) \
			-e POSTGRES_USER=$(PG_USER) \
			-e POSTGRES_PASSWORD=$(PG_PASSWORD) \
			-e POSTGRES_DB=$(PG_DB) \
			-p $(PG_PORT):5432 \
			$(PG_IMAGE) >/dev/null; \
		echo "Waiting for Postgres to accept connections..."; \
		for i in 1 2 3 4 5 6 7 8 9 10; do \
			if docker exec $(PG_CONTAINER_NAME) pg_isready -U $(PG_USER) -d $(PG_DB) >/dev/null 2>&1; then \
				break; \
			fi; \
			sleep 1; \
		done; \
	fi
	MCPKIT_EVENTS_TEST_PGHOST=localhost \
	MCPKIT_EVENTS_TEST_PGPORT=$(PG_PORT) \
	MCPKIT_EVENTS_TEST_PGUSER=$(PG_USER) \
	MCPKIT_EVENTS_TEST_PGPASSWORD=$(PG_PASSWORD) \
	MCPKIT_EVENTS_TEST_PGDB=$(PG_DB) \
	go test ./... -count=1 -timeout 120s

# Start a long-running Postgres container for ad-hoc test iteration.
# Keep until explicitly stopped via `make downdb`.
updb:
	@if docker ps --format '{{.Names}}' | grep -q '^$(PG_CONTAINER_NAME)$$'; then \
		echo "Postgres container $(PG_CONTAINER_NAME) already running."; \
	else \
		echo "Starting Postgres container $(PG_CONTAINER_NAME) on port $(PG_PORT)..."; \
		docker run --rm -d \
			--name $(PG_CONTAINER_NAME) \
			-e POSTGRES_USER=$(PG_USER) \
			-e POSTGRES_PASSWORD=$(PG_PASSWORD) \
			-e POSTGRES_DB=$(PG_DB) \
			-p $(PG_PORT):5432 \
			$(PG_IMAGE); \
	fi

downdb:
	@docker stop $(PG_CONTAINER_NAME) 2>/dev/null || echo "Container $(PG_CONTAINER_NAME) was not running."

dblogs:
	@docker logs -f $(PG_CONTAINER_NAME)
