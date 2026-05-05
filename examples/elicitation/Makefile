demo: ## Run the demo interactively
	go run . --tui

serve: ## Start the MCP server standalone (for MCPJam or other clients)
	go run . --serve

readme: ## Regenerate WALKTHROUGH.md from demo definitions
	go run . --doc md > WALKTHROUGH.md

build: ## Build the example binary
	go build -o elicitation-example .

.PHONY: demo serve readme build
.DEFAULT_GOAL := demo
