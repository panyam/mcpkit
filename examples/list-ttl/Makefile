serve: ## Start the list-ttl demo server (default :8080, ttl=60)
	go run . --serve --ttl 60

build: ## Build the binary
	go build -o list-ttl-demo .

.PHONY: serve build
.DEFAULT_GOAL := serve
