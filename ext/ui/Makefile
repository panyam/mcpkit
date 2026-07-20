# ext/ui Makefile — recipe bodies live in ext/ui/scripts/*.sh (runner-agnostic,
# shared with the justfile).

test: ## Run Go + JS bridge unit tests
	@bash scripts/test.sh

test-bridge: ## Run JS bridge unit tests (requires pnpm + Node.js)
	@bash scripts/test-bridge.sh

test-bridge-e2e: ## Run Playwright bridge integration tests (requires Chromium)
	@bash scripts/test-bridge-e2e.sh

build-bridge: ## Compile mcp-app-bridge.ts → .js (requires pnpm + TypeScript)
	@bash scripts/build-bridge.sh

.PHONY: test test-bridge test-bridge-e2e build-bridge
.DEFAULT_GOAL := test
