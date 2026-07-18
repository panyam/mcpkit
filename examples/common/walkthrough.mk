# examples/common/walkthrough.mk
#
# Shared Makefile fragment for example fixtures that ship a demokit
# walkthrough. Per-fixture Makefile is a one-liner:
#
#   include ../../../common/walkthrough.mk     (compat fixture: 3 dirs deep)
#   include ../../common/walkthrough.mk        (non-compat: 2 dirs deep)
#
# Or set FIXTURE_NAME / SERVER_PORT to override the defaults:
#
#   FIXTURE_NAME := my-fixture-demo
#   SERVER_PORT  := 3201
#   include ../../../common/walkthrough.mk
#
# Defaults:
#   FIXTURE_NAME → directory basename + "-demo" (e.g. basic-vanillajs-demo)
#   SERVER_PORT  → 3101 (the compat-fixture default)

FIXTURE_NAME ?= $(notdir $(CURDIR))-demo
SERVER_PORT  ?= 3101

demo: ## Run the demokit walkthrough (interactive TUI; requires `make serve` in another terminal)
	go run . --demo --tui

note: ## Run the walkthrough in notebook mode (Bubble Tea cells)
	go run . --demo --note

serve: ## Start the fixture as an MCP server
	EXT_APPS_DIR=$${EXT_APPS_DIR:-/tmp/ext-apps} PORT=$${PORT:-$(SERVER_PORT)} go run .

readme: ## Regenerate WALKTHROUGH.md from the walkthrough definitions
	go run . --demo --doc md > WALKTHROUGH.md

record: ## Capture a trace of the walkthrough (interactive — press Enter to advance each step; requires `make serve` in another terminal)
	go run . --demo --tui --record walkthrough.trace.json
	@echo ""
	@echo "Trace written to walkthrough.trace.json — commit it (it's the source of truth for the bundle)."

bundle: ## Build a self-contained HTML player from the recorded trace (output: bundle/index.html + sibling JS/CSS). Commit bundle/ — docs-site picks it up.
	@mkdir -p bundle
	go run . --demo --doc bundle --from walkthrough.trace.json --out bundle/index.html
	@echo "Wrote bundle/index.html — commit bundle/ so docs-site can publish it."

walkthrough: bundle ## Alias for `make bundle`
walk: bundle ## Short alias for `make bundle`

build: ## Build the fixture binary
	go build -o $(FIXTURE_NAME) .

.PHONY: demo note serve readme record bundle walkthrough walk build
.DEFAULT_GOAL := demo
