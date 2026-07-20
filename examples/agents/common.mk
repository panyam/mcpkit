# Shared recipes for agent examples. Include from an example Makefile:
#   include ../common.mk
# `demo` resolves model/base-url from ../llm.json when MODEL/BASE_URL unset
# (see ../demo.sh); keys are never in the file, only apiKeyEnv names.

agent: ## Run the deterministic scripted scenario (no LLM)
	go run .

demo: ## Run against a live model (MODEL/BASE_URL override llm.json)
	@../demo.sh

test: ## Run the golden-transcript test
	go test ./... -count=1

.PHONY: agent demo test
