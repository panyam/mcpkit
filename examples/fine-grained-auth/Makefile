run: ## Run the demo interactively (requires Keycloak — see 'make kcl')
	go run . --tui

serve: ## Start the MCP server standalone (for MCPJam or other clients)
	go run . --serve

kcl: ## Start Keycloak from repo root (convenience wrapper)
	$(MAKE) -C ../.. upkcl

readme: ## Regenerate README.md from demo definitions
	go run . --readme > README.md

build: ## Build the example binary
	go build -o fine-grained-auth-example .

.PHONY: run serve kcl readme build
.DEFAULT_GOAL := run
