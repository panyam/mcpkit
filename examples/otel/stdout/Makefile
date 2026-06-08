EXPORTER ?= stdout

demo: ## Run the demokit walkthrough (TUI). EXPORTER=stdout|otlp (default stdout).
	go run . --tui --exporter=$(EXPORTER)

note: ## Run the walkthrough in notebook mode (Bubble Tea cells). EXPORTER=stdout|otlp (default stdout).
	go run . --note --exporter=$(EXPORTER)

serve: ## Start the otel-stdout demo server on :8080. EXPORTER=stdout|otlp (default stdout).
	go run . --serve --exporter=$(EXPORTER)

readme: ## Regenerate WALKTHROUGH.md from demo definitions
	go run . --doc md > WALKTHROUGH.md

build: ## Build the binary
	go build -o otel-stdout-demo .

test: ## Run the e2e smoke test
	go test ./... -count=1 -timeout 60s

.PHONY: demo note serve readme build test
.DEFAULT_GOAL := demo
