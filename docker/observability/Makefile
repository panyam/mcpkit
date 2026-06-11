# observability stack — OTel Collector + Tempo + Loki + Mimir + Grafana.
# Run from this directory: `cd docker/observability && make up`.

COMPOSE := docker compose -f docker-compose.yml

up: ## Start the observability stack in the background. Grafana lands at http://localhost:3000.
	$(COMPOSE) up -d
	@echo ""
	@echo "  Grafana UI:  http://localhost:3000  (anonymous Admin, no login)"
	@echo "  OTLP gRPC:   localhost:4317"
	@echo "  OTLP HTTP:   localhost:4318"
	@echo ""

down: ## Stop and remove the observability stack containers.
	$(COMPOSE) down

logs: ## Tail logs from all observability containers (Ctrl-C to exit).
	$(COMPOSE) logs -f

build: ## Build any custom images referenced by the stack. No-op today; reserved for future custom images.
	$(COMPOSE) build

ps: ## Show running containers for the observability stack.
	$(COMPOSE) ps

config: ## Validate the compose file syntax without bringing anything up.
	$(COMPOSE) config --quiet
	@echo "docker-compose.yml: OK"

.PHONY: up down logs build ps config
.DEFAULT_GOAL := up
