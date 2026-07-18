# examples/getting-started — the minimal quickstart used by the Get Started
# guide. Not a demokit walkthrough (see README); just a server + client pair.

.PHONY: server client build

server: ## Start the minimal MCP server on :8787
	go run ./server

client: ## Run the minimal MCP client against the server
	go run ./client

build: ## Build both binaries
	go build ./...

.DEFAULT_GOAL := server
