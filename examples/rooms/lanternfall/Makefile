# examples/rooms/lanternfall — recipe bodies live in scripts/*.sh (C8).

run: ## Start the game on :8080 (open http://localhost:8080/signin?as=alice&game=demo)
	go run .

serve: run

test: ## Go tests
	go test ./...

build-web: ## Rebuild static/app.js from web/ (requires pnpm + Node)
	@bash scripts/build-web.sh

check-web: ## Typecheck web/ and fail if static/app.js is stale
	@bash scripts/check-web.sh

test-e2e: ## Drive a player's page in Chromium; also runs the LangGraph game master when ANTHROPIC_API_KEY is set
	@bash scripts/test-e2e.sh

gm: ## Run the LangGraph game master for PLAYER (default alice) in GAME (default demo)
	@cd web && pnpm gm --player $(or $(PLAYER),alice) --game $(or $(GAME),demo)

.PHONY: run serve test build-web check-web test-e2e gm
.DEFAULT_GOAL := run
