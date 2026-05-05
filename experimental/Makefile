# Experimental POC test orchestration.
#
# Runs as one umbrella (`make test`) or per sub-suite. The top-level
# repo Makefile delegates to these targets via `make -C experimental ...`,
# and testall uses the per-suite targets to record sub-stage timing
# (7a/7b/7c/7d) instead of a single bucket.
#
# Two of the sub-suites live under `examples/events/` rather than
# `experimental/`, but they're conceptually part of the experimental
# events POC (they're demo apps consuming the experimental library), so
# they're orchestrated here next to the library tests.

EXPERIMENTAL_DIR := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))
REPO_ROOT := $(abspath $(EXPERIMENTAL_DIR)/..)

.PHONY: test test-events test-events-clients-go test-events-discord test-events-telegram help

test: test-events test-events-clients-go test-events-discord test-events-telegram ## Run all experimental POC tests

test-events: ## Run experimental ext/events library tests
	cd $(EXPERIMENTAL_DIR)/ext/events && go test ./... -count=1 -timeout 120s

test-events-clients-go: ## Run experimental ext/events Go client SDK tests
	cd $(EXPERIMENTAL_DIR)/ext/events/clients/go && go test ./... -count=1 -timeout 60s

test-events-discord: ## Run experimental events Discord example tests
	cd $(REPO_ROOT)/examples/events/discord && go test ./... -count=1 -timeout 60s

test-events-telegram: ## Run experimental events Telegram example tests
	cd $(REPO_ROOT)/examples/events/telegram && go test ./... -count=1 -timeout 60s

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-30s\033[0m %s\n", $$1, $$2}'
