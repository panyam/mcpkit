# ext/ui Makefile

test: ## Run Go + JS bridge unit tests
	go test ./... -count=1 -timeout 30s
	cd assets && pnpm install --frozen-lockfile --silent && pnpm test

test-bridge: ## Run JS bridge unit tests (requires pnpm + Node.js)
	cd assets && pnpm install --frozen-lockfile --silent && pnpm test

test-bridge-e2e: ## Run Playwright bridge integration tests (requires Chromium)
	cp assets/mcp-app-bridge.js tests/playwright/mcp-app-bridge.js
	cd tests/playwright && pnpm install --frozen-lockfile --silent && pnpm test

build-bridge: ## Compile mcp-app-bridge.ts → .js (requires pnpm + TypeScript)
	cd assets && pnpm install --frozen-lockfile && pnpm build

.PHONY: test test-bridge build-bridge
.DEFAULT_GOAL := test
