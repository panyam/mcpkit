run: ## Run the demo interactively
	go run . --tui

serve: ## Start the MCP server standalone (for MCPJam or other clients)
	go run . --serve

readme: ## Regenerate README.md from demo definitions
	go run . --readme > README.md

build: ## Build the example binary
	go build -o elicitation-example .

.PHONY: run serve readme build
.DEFAULT_GOAL := run
