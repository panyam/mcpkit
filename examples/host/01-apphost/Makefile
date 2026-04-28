# 01 — AppHost Basics
#
# Pure Go, in-process. No external dependencies.

EXAMPLE_DIR := 01-apphost

run: ## Interactive — pause between steps
	cd .. && go run -buildvcs=false ./$(EXAMPLE_DIR)/

demo: ## Non-interactive — full output
	cd .. && go run -buildvcs=false ./$(EXAMPLE_DIR)/ --non-interactive

readme: ## Regenerate README.md from code
	cd .. && go run -buildvcs=false ./$(EXAMPLE_DIR)/ --readme > $(EXAMPLE_DIR)/README.md
