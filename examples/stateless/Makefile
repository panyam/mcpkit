serve: ## Start the stateless-demo server (default :8080, pure stateless wire)
	go run . --serve

serve-dual: ## Start the server in Dual mode (legacy + stateless on one URL)
	go run . --serve --mode=dual

build: ## Build the binary
	go build -o stateless-demo .

.PHONY: serve serve-dual build
.DEFAULT_GOAL := serve
