serve: ## Start the skills-demo server in file mode (default :8080)
	go run . --serve

serve-archive: ## Start the server in archive mode (.tar.gz per skill)
	go run . --serve --mode=archive

serve-zip: ## Start the server in archive mode (.zip per skill)
	go run . --serve --mode=zip

build: ## Build the binary to ./skills-demo
	go build -o skills-demo .

.PHONY: serve serve-archive serve-zip build
.DEFAULT_GOAL := serve
