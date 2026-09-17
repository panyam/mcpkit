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

# Reaches scripts/ through the environment (C8: recipes dispatch, scripts do the work).
export PG_CONTAINER_NAME PG_PORT PG_USER PG_PASSWORD PG_DB PG_IMAGE

.PHONY: test testsqlite testpg updb downdb dblogs

# Default: sqlite + inmemory only. No Docker required, fast.
test: testsqlite

testsqlite:
	go test ./... -count=1 -timeout 60s

# Run the full conformance matrix including the Postgres backend.
testpg:
	@./scripts/pg.sh test

# Start a long-running Postgres container for ad-hoc test iteration.
# Keep until explicitly stopped via `make downdb`.
updb:
	@./scripts/pg.sh up

downdb:
	@docker stop $(PG_CONTAINER_NAME) 2>/dev/null || echo "Container $(PG_CONTAINER_NAME) was not running."

dblogs:
	@docker logs -f $(PG_CONTAINER_NAME)
