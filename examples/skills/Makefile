
demo: ## Run the demokit walkthrough (text, non-interactive). Server must be up via `make serve` in another terminal.
	go run . --tui

serve: ## Start the skills-demo server with local + archive + github sources layered (best-effort github)
	go run . --serve --source=multi

serve-archive: ## Start the server in archive mode (.tar.gz per skill)
	go run . --serve --bundle=archive

serve-zip: ## Start the server in archive mode (.zip per skill)
	go run . --serve --bundle=zip

# --- Source adapters (#797) ---
# `make serve` is the headline demo: local + archive + github sources
# layered under distinct URI prefixes (local/, archived/, github/).
# The github layer is best-effort — soft-fails on no-network so the
# rest still runs. Individual sources are accessible via the binary's
# --source flag for debugging:
#     go run . --serve --source=dir                # plain local dir (default in binary)
#     go run . --serve --source=archive --source-path=<file>
#     go run . --serve --source=archives-dir --source-path=<dir>
#     go run . --serve --source=github --source-github=owner/repo[@ref][:subdir]

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
