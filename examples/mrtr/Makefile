demo: ## Run the demokit walkthrough (interactive, TUI)
	go run . --tui

serve: ## Start the MRTR demo server (default :8080)
	go run . --serve

readme: ## Regenerate WALKTHROUGH.md from demo definitions
	go run . --readme > WALKTHROUGH.md

build: ## Build the binary
	go build -o mrtr-demo .

.PHONY: demo serve readme build
.DEFAULT_GOAL := demo
