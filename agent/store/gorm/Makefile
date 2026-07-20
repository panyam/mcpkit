# Makefile for agent/store/gorm
#
# `make test`   — fast path: sqlite (no Docker required). Default for CI.
# `make testpg` — boots a Postgres container if missing, then runs the
#                 matrix (sqlite + postgres).
# `make updb`   — start the Postgres container (long-running).
# `make downdb` — stop the Postgres container.
#
# Port 5435: the events gorm store's test container owns 5434, and 5432/
# 5433 are left for whatever the operator runs.

PG_CONTAINER_NAME := mcpkit-agent-gorm-test-pg
PG_PORT           := 5435
PG_USER           := postgres
PG_PASSWORD       := testpassword
PG_DB             := mcpkit_agent_test
# pgvector image (superset of Postgres) so `testpg` also covers the pgvector
# semantic MemoryStore; the semantic tests CREATE EXTENSION vector themselves
# and skip if unavailable, so a non-pgvector Postgres still runs the rest.
PG_IMAGE          := pgvector/pgvector:pg16

.PHONY: test testpg updb downdb

test:
	go test ./... -count=1 -timeout 60s

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
	MCPKIT_AGENT_TEST_PGHOST=localhost \
	MCPKIT_AGENT_TEST_PGPORT=$(PG_PORT) \
	MCPKIT_AGENT_TEST_PGUSER=$(PG_USER) \
	MCPKIT_AGENT_TEST_PGPASSWORD=$(PG_PASSWORD) \
	MCPKIT_AGENT_TEST_PGDB=$(PG_DB) \
	go test ./... -count=1 -timeout 120s

updb:
	@if docker ps --format '{{.Names}}' | grep -q '^$(PG_CONTAINER_NAME)$$'; then \
		echo "Postgres container $(PG_CONTAINER_NAME) already running."; \
	else \
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
