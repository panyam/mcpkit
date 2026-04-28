run: ## Run the demo interactively
	go run .

readme: ## Regenerate README.md from demo definitions
	go run . --readme > README.md

build: ## Build the example binary
	go build -o elicitation-example .

.PHONY: run readme build
.DEFAULT_GOAL := run
