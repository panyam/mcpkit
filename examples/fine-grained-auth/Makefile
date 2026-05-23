demo: ## Run the demo interactively (requires Keycloak — see 'make kcl')
	go run . --tui

note: ## Run the walkthrough in notebook mode (requires Keycloak — see 'make kcl')
	go run . --note

serve: ## Start the MCP server standalone (for MCPJam or other clients)
	go run . --serve

kcl: ## Start Keycloak from repo root (convenience wrapper)
	$(MAKE) -C ../.. upkcl

readme: ## Regenerate WALKTHROUGH.md from demo definitions
	go run . --doc md > WALKTHROUGH.md

build: ## Build the example binary
	go build -o fine-grained-auth-example .

.PHONY: demo note serve kcl readme build
.DEFAULT_GOAL := demo
