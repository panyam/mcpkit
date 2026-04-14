# ext/ui Makefile

test: ## Run unit tests
	go test ./... -count=1 -timeout 30s

build-bridge: ## Compile mcp-app-bridge.ts → .js (requires pnpm + TypeScript)
	cd assets && pnpm install --frozen-lockfile && pnpm build

.PHONY: test build-bridge
.DEFAULT_GOAL := test
