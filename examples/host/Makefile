# Host Examples — top-level Makefile
# Generates READMEs and runs all examples non-interactively.

EXAMPLES := 01-apphost 02-multi-server

.PHONY: readme demo

readme: ## Regenerate all READMEs from code
	@for ex in $(EXAMPLES); do \
		echo "=== Generating $$ex/README.md ==="; \
		go run -buildvcs=false ./$$ex/ --readme > $$ex/README.md; \
	done

demo: ## Run all examples non-interactively
	@for ex in $(EXAMPLES); do \
		echo "=== Running $$ex ==="; \
		go run -buildvcs=false ./$$ex/ --non-interactive; \
		echo; \
	done
