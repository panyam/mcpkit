run: ## Run the demokit walkthrough (interactive, TUI)
	go run . --tui

serve: ## Start the v2 tasks server standalone (for MCPJam, VS Code, Claude Desktop)
	go run . --serve

readme: ## Regenerate WALKTHROUGH.md from demo definitions
	go run . --readme > WALKTHROUGH.md

build: ## Build the binary
	go build -o tasks-v2-demo .

.PHONY: run serve readme build
.DEFAULT_GOAL := run
