
demo: ## Run the demokit walkthrough (text, non-interactive). Server must be up via `make serve` in another terminal.
	go run . --tui

serve: ## Start the skills-demo server in file mode (default :8080)
	go run . --serve

serve-archive: ## Start the server in archive mode (.tar.gz per skill)
	go run . --serve --mode=archive

serve-zip: ## Start the server in archive mode (.zip per skill)
	go run . --serve --mode=zip

run: ## Run the walkthrough in interactive TUI mode (paginated, key-driven).
	go run .

note: ## Run the walkthrough in notebook mode (Bubble Tea cells).
	go run . --note

readme: ## Regenerate WALKTHROUGH.md from the demo definitions.
	go run . --doc=md > WALKTHROUGH.md

fetch-docx: ## Stage Anthropic's docx skill from github.com/anthropics/skills into skills/ for a real-world demo.
	bash scripts/fetch-docx-skill.sh

build: ## Build the binary to ./skills-demo
	go build -o skills-demo .

.PHONY: serve serve-archive serve-zip demo tui note readme fetch-docx build
.DEFAULT_GOAL := serve
