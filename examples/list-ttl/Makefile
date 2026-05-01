demo: ## Run the demokit walkthrough (interactive, TUI)
	go run . --tui

serve: ## Start the list-ttl demo server (default :8080, ttl=60)
	go run . --serve --ttl=60

readme: ## Regenerate WALKTHROUGH.md from demo definitions
	go run . --readme > WALKTHROUGH.md

build: ## Build the binary
	go build -o list-ttl-demo .

.PHONY: demo serve readme build
.DEFAULT_GOAL := demo
