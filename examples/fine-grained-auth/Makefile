run: ## Start the fine-grained auth example (requires Keycloak: make upkcl from repo root)
	go run . -addr :8087

build: ## Build the example binary
	go build -o fine-grained-auth-example .

.PHONY: run build
.DEFAULT_GOAL := run
