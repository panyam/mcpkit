# examples/getting-started — the minimal quickstart used by the Get Started
# guide. Not a demokit walkthrough (see README); just a server + client pair.

.PHONY: server client build test

server: ## Start the minimal MCP server on :8787
	go run ./server

client: ## Run the minimal MCP client against the server
	go run ./client

build: ## Build both binaries
	go build ./...

test: ## Run the scenario natively and under js/wasm (needs node)
	$(MAKE) -C .. test-getting-started

.DEFAULT_GOAL := server

include ../common/playground.mk
