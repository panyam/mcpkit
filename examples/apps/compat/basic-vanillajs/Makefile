demo: ## Run the demokit walkthrough (scripted MCP client; requires `make serve` in another terminal)
	go run . --demo --tui

note: ## Run the walkthrough in notebook mode (Bubble Tea cells)
	go run . --demo --note

serve: ## Start the basic-vanillajs compat fixture (default :3101)
	EXT_APPS_DIR=$${EXT_APPS_DIR:-/tmp/ext-apps} PORT=$${PORT:-3101} go run .

readme: ## Regenerate WALKTHROUGH.md from the walkthrough definitions
	go run . --demo --doc md > WALKTHROUGH.md

record: ## Capture a trace of the walkthrough (interactive — press Enter to advance each step; requires `make serve` in another terminal)
	go run . --demo --tui --record walkthrough.trace.json
	@echo ""
	@echo "Trace written to walkthrough.trace.json — commit it (it's the source of truth for the bundle)."

bundle: ## Build a self-contained HTML player from the recorded trace (output: bundle/index.html + sibling JS/CSS). Commit bundle/ — docs-site picks it up from there.
	@mkdir -p bundle
	go run . --demo --doc bundle --from walkthrough.trace.json --out bundle/index.html
	@echo "Wrote bundle/index.html — commit bundle/ so docs-site can publish it."

walkthrough: bundle ## Alias for `make bundle`
walk: bundle ## Short alias for `make bundle`

build: ## Build the binary
	go build -o basic-vanillajs-demo .

.PHONY: demo note serve readme record bundle walkthrough walk build
.DEFAULT_GOAL := demo
