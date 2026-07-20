# Experimental POC test orchestration.
#
# Runs as one umbrella (`make test`) or per sub-suite. The top-level
# repo Makefile delegates to these targets via `make -C experimental ...`,
# and testall uses the per-suite targets to record sub-stage timing
# (7a/7b/7c/7d/7e) instead of a single bucket.
#
# The per-suite bodies live in experimental/scripts/*.sh (runner-agnostic,
# shared with the justfile). The -pg / -real variants delegate to the
# sub-module recipes that boot Docker containers.
#
# Two of the sub-suites live under `examples/events/` rather than
# `experimental/`, but they're conceptually part of the experimental
# events POC (they're demo apps consuming the experimental library), so
# they're orchestrated here next to the library tests.

EXPERIMENTAL_DIR := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))

.PHONY: test test-events test-events-clients-go test-events-stores-gorm test-events-stores-gorm-pg test-events-stores-redis test-events-stores-redis-real test-events-discord test-events-telegram help

test: test-events test-events-clients-go test-events-stores-gorm test-events-discord test-events-telegram ## Run all experimental POC tests

test-events: ## Run experimental ext/events library tests
	@bash $(EXPERIMENTAL_DIR)/scripts/test-events.sh

test-events-clients-go: ## Run experimental ext/events Go client SDK tests
	@bash $(EXPERIMENTAL_DIR)/scripts/test-events-clients-go.sh

test-events-stores-gorm: ## Run GORM-backed Webhook + Quota stores (sqlite + inmemory; no Docker required)
	@bash $(EXPERIMENTAL_DIR)/scripts/test-events-stores-gorm.sh

test-events-stores-gorm-pg: ## Run GORM stores against a real Postgres container (full matrix; needs Docker)
	$(MAKE) -C $(EXPERIMENTAL_DIR)/ext/events/stores/gorm testpg

test-events-stores-redis: ## Run Redis pubsub Emitter sub-module (miniredis; no Docker required)
	@bash $(EXPERIMENTAL_DIR)/scripts/test-events-stores-redis.sh

test-events-stores-redis-real: ## Run the Redis pubsub Emitter against a real Redis container (Docker)
	$(MAKE) -C $(EXPERIMENTAL_DIR)/ext/events/stores/redis testredis

test-events-discord: ## Run experimental events Discord example tests
	@bash $(EXPERIMENTAL_DIR)/scripts/test-events-discord.sh

test-events-telegram: ## Run experimental events Telegram example tests
	@bash $(EXPERIMENTAL_DIR)/scripts/test-events-telegram.sh

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-30s\033[0m %s\n", $$1, $$2}'
