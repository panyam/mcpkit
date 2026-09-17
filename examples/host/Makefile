# Host Examples — top-level Makefile
# Generates READMEs and runs all examples non-interactively.

EXAMPLES := 01-apphost 02-multi-server

# Reaches scripts/ through the environment (C8: recipes dispatch, scripts do the work).
export EXAMPLES

.PHONY: readme demo note

readme: ## Regenerate all READMEs from code
	@./scripts/run-examples.sh readme

demo: ## Run all examples non-interactively
	@./scripts/run-examples.sh demo

note: ## Run each example in notebook mode (Bubble Tea cells), one at a time
	@./scripts/run-examples.sh note
