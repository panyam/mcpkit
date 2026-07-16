# agentchat — terminal chat harness over MCP servers.
BINARY := agentchat

build: ## Build the binary into ./bin
	@mkdir -p bin
	go build -o bin/$(BINARY) .

test: ## Run the test suite
	go test ./... -count=1 -timeout 60s

test-race: ## Run the test suite with the race detector
	go test ./... -count=1 -race -timeout 120s

run: build ## Build then run with CONFIG=<path> (or ARGS="--model m --url u")
	@if [ -n "$(CONFIG)" ]; then ./bin/$(BINARY) --config $(CONFIG); \
	else ./bin/$(BINARY) $(ARGS); fi

install: ## go install into GOPATH/bin
	go install .

clean: ## Remove build artifacts
	rm -rf bin

help: ## Show targets
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-12s %s\n", $$1, $$2}'

.PHONY: build test test-race run install clean help
