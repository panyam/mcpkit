# ext/protogen Makefile

.PHONY: build test lint generate push install help

build: ## Build the protoc plugin
	go build -o bin/protoc-gen-go-mcp ./cmd/protoc-gen-go-mcp

test: ## Run all tests
	go test ./... -count=1 -timeout 30s

install: ## Install the protoc plugin to $GOPATH/bin
	go install ./cmd/protoc-gen-go-mcp

# --- Proto / Buf targets ---

lint: ## Lint proto files with buf
	buf lint

generate: ## Generate Go code from proto files
	buf generate

push: ## Push proto module to buf.build/mcpkit/protogen
	buf push

breaking: ## Check for breaking changes against buf.build
	buf breaking --against buf.build/mcpkit/protogen

dep-update: ## Update buf dependencies
	buf dep update

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-18s\033[0m %s\n", $$1, $$2}'

.DEFAULT_GOAL := help
