# tutorials/walkthrough/Makefile
#
# Operations on the walkthrough pages. Run from this directory.
#
# Each page declares its kind and prerequisites in a header line that looks like:
#   > **Kind:** root *(FAQ-style)* · **Prerequisites:** [bring-up](./bringup.md), [transport-mechanics](./transport-mechanics.md)
#
# The Makefile parses those headers to derive the graph, count pages by kind,
# and surface planned-but-unwritten pages.

# Use bash for process substitution (`<(...)`) and other niceties.
SHELL := /bin/bash

.PHONY: help graph stats missing stubs links check
.DEFAULT_GOAL := help

# Meta files don't appear as graph nodes.
META := README.md STRUCTURE.md INDEX.md GRAPH.md Makefile
PAGES := $(filter-out $(META),$(wildcard *.md))

help: ## Show available targets
	@awk 'BEGIN {FS = ":.*?## "} /^[a-z][a-zA-Z-]+:.*## / {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

graph: ## Auto-generate GRAPH.md from page Prerequisites headers
	@./build-graph.sh > GRAPH.md
	@echo "Wrote GRAPH.md ($$(wc -l < GRAPH.md | tr -d ' ') lines)"

stats: ## Count pages by kind, written/stub status, stub-link references
	@./scripts/lint.sh stats

missing: ## List link targets that don't exist on disk (should be empty since all stubs are created)
	@./scripts/lint.sh missing

stubs: ## List stub pages (header-only, body TBD)
	@grep -l '^<!-- STUB -->' $(PAGES) 2>/dev/null | sort

links: ## List all internal page links (deduped)
	@grep -hoE '\(\./[a-zA-Z-]+\.md[^)]*\)' *.md 2>/dev/null | sort -u

check: ## Verify all internal links resolve to written pages or to the INDEX forthcoming-nodes table
	@./scripts/lint.sh check
