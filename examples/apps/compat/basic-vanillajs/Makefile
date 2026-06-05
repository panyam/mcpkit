demo: ## Run the demokit walkthrough (scripted MCP client; requires `make serve` in another terminal)
	go run . --demo --tui

note: ## Run the walkthrough in notebook mode (Bubble Tea cells)
	go run . --demo --note

serve: ## Start the basic-vanillajs compat fixture (default :3101)
	EXT_APPS_DIR=$${EXT_APPS_DIR:-/tmp/ext-apps} PORT=$${PORT:-3101} go run .

readme: ## Regenerate WALKTHROUGH.md from the walkthrough definitions
	go run . --demo --doc md > WALKTHROUGH.md

record: ## Capture a trace of the walkthrough (interactive — press Enter to advance each step; requires `make serve` in another terminal)
	go run . --demo --tui --record .walkthrough.trace.json
	@echo ""
	@echo "Trace written to .walkthrough.trace.json — commit it (it's the source of truth for the docs-site bundle)."

bundle: record ## Build a self-contained HTML player from the recorded trace (output: walkthrough/index.html + sibling JS/CSS)
	@mkdir -p walkthrough
	go run . --demo --doc bundle --from .walkthrough.trace.json --out walkthrough/index.html
	@echo "Wrote walkthrough/index.html — open it directly or host on gh-pages."

build: ## Build the binary
	go build -o basic-vanillajs-demo .

.PHONY: demo note serve readme record bundle build
.DEFAULT_GOAL := demo
