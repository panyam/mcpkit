demo: ## Run the demo interactively
	go run . --tui

note: ## Run the walkthrough in notebook mode (Bubble Tea cells)
	go run . --note

serve: ## Start the MCP server standalone (for MCPJam or other clients)
	go run . --serve

readme: ## Regenerate WALKTHROUGH.md from demo definitions
	go run . --doc md > WALKTHROUGH.md

build: ## Build the example binary
	go build -o elicitation-example .

.PHONY: demo note serve readme build
.DEFAULT_GOAL := demo
