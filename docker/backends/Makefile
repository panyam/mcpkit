# backends stack — Keycloak + Postgres + Redis.
# Run from this directory: `cd docker/backends && make up`.

COMPOSE := docker compose -f docker-compose.yml

up: ## Start the backends stack in the background. Keycloak admin UI lands at http://localhost:8180.
	$(COMPOSE) up -d
	@echo ""
	@echo "  Keycloak UI: http://localhost:8180  (admin/admin)"
	@echo "  Postgres:    postgres:5432 on the mcpkit network (postgres/postgres, db=events)"
	@echo "  Redis:       redis:6379 on the mcpkit network"
	@echo ""

down: ## Stop and remove the backends stack containers.
	$(COMPOSE) down

logs: ## Tail logs from all backend containers (Ctrl-C to exit).
	$(COMPOSE) logs -f

build: ## Build any custom images referenced by the stack. No-op today; reserved for future custom images.
	$(COMPOSE) build

ps: ## Show running containers for the backends stack.
	$(COMPOSE) ps

config: ## Validate the compose file syntax without bringing anything up.
	$(COMPOSE) config --quiet
	@echo "docker-compose.yml: OK"

.PHONY: up down logs build ps config
.DEFAULT_GOAL := up
