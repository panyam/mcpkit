demo: ## Run the demokit walkthrough (interactive, TUI)
	go run . --tui

serve: ## Start the file-inputs demo server (default :8080)
	go run . --serve

readme: ## Regenerate WALKTHROUGH.md from demo definitions
	go run . --doc md > WALKTHROUGH.md

build: ## Build the binary
	go build -o file-inputs-demo .

.PHONY: demo serve readme build
.DEFAULT_GOAL := demo
