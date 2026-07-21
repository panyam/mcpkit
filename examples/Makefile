# examples/Makefile — test orchestration for the examples tree.
#
# The repo Makefile delegates here via `make test-examples` (-> `make -C
# examples test`). Mirrors conformance/Makefile: one `test` umbrella plus
# per-example targets, each runnable directly from this directory.
#
# Every target here is infra-free (no Docker/live services) so `test` runs in
# CI as-is. Examples that need containers or credentials to exercise fully
# (auth/keycloak, whole-enchilada stacks) are covered by their own suites, not
# this umbrella.
#
# The events/discord and events/telegram example tests moved here from
# experimental/Makefile (they test example apps, not the experimental library);
# experimental/Makefile keeps the old target names as aliases so testall and CI
# scripts keep working.

EXAMPLES_DIR := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))

.PHONY: test help \
  test-agents-async test-agents-multi test-skills test-tasks-v2 \
  test-common test-otel-stdout test-protogen \
  test-events-kitchen-sink test-events-discord test-events-telegram \
  test-whole-enchilada-event-server

test: test-agents-async test-agents-multi test-skills test-tasks-v2 \
      test-common test-otel-stdout test-protogen \
      test-events-kitchen-sink test-events-discord test-events-telegram \
      test-whole-enchilada-event-server ## Run every infra-free example test

test-agents-async: ## agents/agent-async
	cd $(EXAMPLES_DIR)/agents/agent-async && go test ./... -count=1 -timeout 60s

test-agents-multi: ## agents/multi-agent
	cd $(EXAMPLES_DIR)/agents/multi-agent && go test ./... -count=1 -timeout 60s

test-skills: ## skills (agent scenario)
	cd $(EXAMPLES_DIR)/skills && go test ./... -count=1 -timeout 60s -run TestAgentScenario

test-tasks-v2: ## tasks-v2 (agent scenario)
	cd $(EXAMPLES_DIR)/tasks-v2 && go test ./... -count=1 -timeout 60s -run TestAgentScenario

test-common: ## common (shared example helpers + otel setup)
	cd $(EXAMPLES_DIR)/common && go test ./... -count=1 -timeout 60s

test-otel-stdout: ## otel/stdout smoke
	cd $(EXAMPLES_DIR)/otel/stdout && go test ./... -count=1 -timeout 60s

test-protogen: ## protogen/bookservice
	cd $(EXAMPLES_DIR)/protogen/bookservice && go test ./... -count=1 -timeout 60s

test-events-kitchen-sink: ## events/kitchen-sink
	cd $(EXAMPLES_DIR)/events/kitchen-sink && go test ./... -count=1 -timeout 60s

test-whole-enchilada-event-server: ## whole-enchilada/events/event-server
	cd $(EXAMPLES_DIR)/whole-enchilada/events/event-server && go test ./... -count=1 -timeout 60s

test-events-discord: ## events/discord (runner-agnostic script, shared with the justfile)
	@bash $(EXAMPLES_DIR)/../experimental/scripts/test-events-discord.sh

test-events-telegram: ## events/telegram
	@bash $(EXAMPLES_DIR)/../experimental/scripts/test-events-telegram.sh

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-34s\033[0m %s\n", $$1, $$2}'
