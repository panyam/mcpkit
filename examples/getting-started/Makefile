# examples/getting-started — the minimal quickstart used by the Get Started
# guide. Not a demokit walkthrough (see README); just a server + client pair.

.PHONY: server client build test web serve-web

server: ## Start the minimal MCP server on :8787
	go run ./server

client: ## Run the minimal MCP client against the server
	go run ./client

build: ## Build both binaries
	go build ./...

test: ## Run the scenario natively and under js/wasm (needs node)
	$(MAKE) -C .. test-getting-started

web: ## Build the server for the browser into web/dist/
	@bash ../../scripts/build-example-wasm.sh . ./server

serve-web: ## Build the browser page and serve it on http://localhost:8788/
	@bash ../../scripts/serve-example-wasm.sh . ./server 8788

.DEFAULT_GOAL := server
