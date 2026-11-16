demo: ## Run the demokit walkthrough (interactive, TUI)
	go run . --tui

note: ## Run the walkthrough in notebook mode (Bubble Tea cells)
	go run . --note

serve: ## Start the MRTR demo server (default :8080)
	go run . --serve

readme: ## Regenerate WALKTHROUGH.md from demo definitions
	go run . --doc md > WALKTHROUGH.md

build: ## Build the binary
	go build -o mrtr-demo .

serve-web: ## Serve the browser playground on http://localhost:8788/
	@bash ../../scripts/serve-example-wasm.sh . ./web 8788

.PHONY: demo note serve readme build serve-web
.DEFAULT_GOAL := demo
