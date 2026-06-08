# Top-level docker tooling — orchestrates compose files under ./<stack>/.
# Today there is just one stack (./observability/) but the target names
# stay short (up / down / logs / build) so the common interaction is
# `make -C docker up`. When a second stack lands, rename to
# obs-up / obs-down / etc. and add the sibling target set; the
# transition cost is small and avoids over-engineering namespacing
# upfront.

STACK ?= observability
COMPOSE := docker compose -f $(STACK)/docker-compose.yml

up: ## Start the $(STACK) stack in the background. Grafana lands at http://localhost:3000.
	$(COMPOSE) up -d
	@echo ""
	@echo "  Grafana UI:  http://localhost:3000  (anonymous Admin, no login)"
	@echo "  OTLP gRPC:   localhost:4317"
	@echo "  OTLP HTTP:   localhost:4318"
	@echo ""

down: ## Stop and remove the $(STACK) stack containers.
	$(COMPOSE) down

logs: ## Tail logs from all $(STACK) containers (Ctrl-C to exit).
	$(COMPOSE) logs -f

build: ## Build any custom images referenced by the $(STACK) stack. No-op today; reserved for future custom images.
	$(COMPOSE) build

ps: ## Show running containers for the $(STACK) stack.
	$(COMPOSE) ps

config: ## Validate the $(STACK) compose file syntax without bringing anything up.
	$(COMPOSE) config --quiet
	@echo "$(STACK)/docker-compose.yml: OK"

.PHONY: up down logs build ps config
.DEFAULT_GOAL := up
