# bookservice example Makefile

.PHONY: generate run test clean help

generate: ## Generate Go code from proto files
	rm -rf gen
	buf generate

run: ## Run the example MCP server
	go run .

test: ## Run e2e tests
	go test -count=1 -timeout 30s -v .

clean: ## Remove generated artifacts
	rm -rf gen

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-18s\033[0m %s\n", $$1, $$2}'

.DEFAULT_GOAL := help
