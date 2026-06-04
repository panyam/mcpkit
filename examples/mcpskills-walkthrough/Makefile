demo: ## Run the walkthrough in text mode (default, non-interactive once stepped through).
	go run .

tui: ## Run the walkthrough in interactive TUI mode (paginated, key-driven).
	go run . --tui

note: ## Run the walkthrough in notebook mode (Bubble Tea cells).
	go run . --note

smoke: ## Run the walkthrough non-interactively as a CI smoke test (asserts exit 0).
	go run . --non-interactive

readme: ## Regenerate WALKTHROUGH.md from the demo definitions.
	go run . --doc=md > WALKTHROUGH.md

build: ## Build the example binary to ./mcpskills-walkthrough.
	go build -o mcpskills-walkthrough .

.PHONY: demo tui note smoke readme build
.DEFAULT_GOAL := demo
