# agent-async — an agent managing async work (events + tasks) through chat.

agent: ## Run the deterministic scripted scenario (no LLM)
	go run .

demo: ## Run against a live model (MODEL=<name> [BASE_URL=<url>])
	go run . --model $(MODEL) $(if $(BASE_URL),--base-url $(BASE_URL))

test: ## Run the golden-transcript test
	go test ./... -count=1

.PHONY: agent demo test
