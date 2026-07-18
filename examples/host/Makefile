# Host Examples — top-level Makefile
# Generates READMEs and runs all examples non-interactively.

EXAMPLES := 01-apphost 02-multi-server

.PHONY: readme demo note

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

note: ## Run each example in notebook mode (Bubble Tea cells), one at a time
	@for ex in $(EXAMPLES); do \
		echo "=== Running $$ex (notebook) ==="; \
		go run -buildvcs=false ./$$ex/ --note; \
		echo; \
	done
