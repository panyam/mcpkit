ADDR ?= http://localhost:8080
MCP  := $(ADDR)/mcp

.PHONY: help serve demo demo-test note readme build test test-race clean

help: ## Show this help.
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / { printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

serve: ## Start the kitchen-sink server standalone.
	go run . --serve -addr :8080

demo: ## Run the demokit walkthrough (interactive TUI). Server must be up via `make serve` in another terminal.
	go run . --tui

demo-test: ## Run the walkthrough non-interactive (CI / scripting).
	go run . --non-interactive

note: ## Run the walkthrough in notebook mode (Bubble Tea cells).
	go run . --note

readme: ## Regenerate WALKTHROUGH.md from walkthrough.go definitions.
	go run . --doc=md > WALKTHROUGH.md

build: ## Build the kitchen-sink binary.
	go build -o kitchen-sink-events .

test: ## In-process e2e tests for Match / Transform / OnSubscribe / EmitToSubscription / Quota.
	go test ./... -count=1 -v

test-race: ## Tests under -race.
	go test ./... -race -count=1

clean: ## Remove built binaries.
	rm -f kitchen-sink-events
