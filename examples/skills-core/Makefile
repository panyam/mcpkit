serve: ## Start the skills-core server (file mode, :8080)
	go run . --serve

demo: ## Run the walkthrough in interactive TUI. Server must be up via `make serve` in another terminal.
	go run . --tui

run: ## Run the walkthrough (plain, non-interactive-friendly).
	go run .

note: ## Run the walkthrough in notebook mode (Bubble Tea cells).
	go run . --note

readme: ## Regenerate WALKTHROUGH.md from the demo definitions.
	go run . --doc=md > WALKTHROUGH.md

build: ## Build the binary to ./skills-core
	go build -o skills-core .

.PHONY: serve demo run note readme build
.DEFAULT_GOAL := serve
