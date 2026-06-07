demo: ## Run the demokit walkthrough (interactive, TUI)
	go run . --tui

note: ## Run the walkthrough in notebook mode (Bubble Tea cells)
	go run . --note

serve: ## Start the otel-stdout demo server (default :8080)
	go run . --serve

readme: ## Regenerate WALKTHROUGH.md from demo definitions
	go run . --doc md > WALKTHROUGH.md

build: ## Build the binary
	go build -o otel-stdout-demo .

test: ## Run the e2e smoke test
	go test ./... -count=1 -timeout 60s

.PHONY: demo note serve readme build test
.DEFAULT_GOAL := demo
