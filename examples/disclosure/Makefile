demo: ## Run the demokit walkthrough (interactive, TUI). Server must be up via `make serve` in another terminal.
	go run . --tui

note: ## Run the walkthrough in notebook mode (Bubble Tea cells)
	go run . --note

serve: ## Start the fixture server (200 tools + 2 skills on :8080)
	go run . --serve

check: ## Run the exercise checks against a running server (URL=http://localhost:8080)
	go run . --check --url $(or $(URL),http://localhost:8080)

test: ## Run the plain-client checks on both wires
	go test ./... -count=1

readme: ## Regenerate WALKTHROUGH.md from demo definitions
	go run . --doc md > WALKTHROUGH.md

build: ## Build the binary
	go build -o disclosure-demo .

.PHONY: demo note serve check test readme build
.DEFAULT_GOAL := demo
