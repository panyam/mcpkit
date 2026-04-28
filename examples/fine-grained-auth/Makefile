run: ## Run the demo interactively (requires Keycloak: make upkcl from repo root)
	go run . --tui

serve: ## Start the MCP server standalone (for MCPJam or other clients)
	go run . --serve

readme: ## Regenerate README.md from demo definitions
	go run . --readme > README.md

build: ## Build the example binary
	go build -o fine-grained-auth-example .

.PHONY: run serve readme build
.DEFAULT_GOAL := run
