# examples/apps/any-frontend — recipe bodies live in scripts/*.sh (C8).

run: ## Start the server on :8080 (open http://localhost:8080/host)
	go run .

serve: run

test: ## Go unit tests
	go test ./...

build-views: ## Rebuild views/*.html from web/ (requires pnpm + Node)
	@bash scripts/build-views.sh

check-views: ## Typecheck web/ and fail if views/*.html are stale
	@bash scripts/check-views.sh

test-e2e: ## Drive all four Views in Chromium through /host
	@bash scripts/test-e2e.sh

.PHONY: run serve test build-views check-views test-e2e
.DEFAULT_GOAL := run
