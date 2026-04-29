run: ## Run the demokit walkthrough (interactive, TUI)
	go run . --tui

serve: ## Start the tasks MCP server standalone (for MCPJam, VS Code, Claude Desktop)
	go run . --serve

readme: ## Regenerate WALKTHROUGH.md from demo definitions
	go run . --readme > WALKTHROUGH.md

runts: ## Run the TS reference server (parity check)
	node ts-reference-server.mjs

build: ## Build the binary
	go build -o tasks-demo .

.PHONY: run serve readme runts build
.DEFAULT_GOAL := run
