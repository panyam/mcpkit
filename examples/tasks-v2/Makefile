demo: ## Run the demokit walkthrough (interactive, TUI)
	go run . --tui

note: ## Run the walkthrough in notebook mode (Bubble Tea cells)
	go run . --note

serve: ## Start the v2 tasks server standalone (for MCPJam, VS Code, Claude Desktop)
	go run . --serve

readme: ## Regenerate WALKTHROUGH.md from demo definitions
	go run . --doc md > WALKTHROUGH.md

build: ## Build the binary
	go build -o tasks-v2-demo .

.PHONY: demo note serve readme build
.DEFAULT_GOAL := demo
