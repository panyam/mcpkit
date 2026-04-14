# ext/ui Makefile

test: ## Run Go unit tests
	go test ./... -count=1 -timeout 30s

test-bridge: ## Run JS bridge unit tests (requires pnpm + Node.js)
	cd assets && pnpm install --frozen-lockfile && pnpm test

build-bridge: ## Compile mcp-app-bridge.ts → .js (requires pnpm + TypeScript)
	cd assets && pnpm install --frozen-lockfile && pnpm build

.PHONY: test test-bridge build-bridge
.DEFAULT_GOAL := test
