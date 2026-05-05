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
	@printf "Content pages:    %s\n" "$$(echo $(PAGES) | wc -w | tr -d ' ')"
	@printf "  roots:          %s\n" "$$(grep -l '\*\*Kind:\*\* root' $(PAGES) 2>/dev/null | wc -l | tr -d ' ')"
	@printf "  branches:       %s\n" "$$(grep -l '\*\*Kind:\*\* branch' $(PAGES) 2>/dev/null | wc -l | tr -d ' ')"
	@printf "  leaves:         %s\n" "$$(grep -l '\*\*Kind:\*\* leaf' $(PAGES) 2>/dev/null | wc -l | tr -d ' ')"
	@printf "  written:        %s\n" "$$( { for f in $(PAGES); do grep -L '^<!-- STUB -->' "$$f"; done; } | wc -l | tr -d ' ')"
	@printf "  stubs:          %s\n" "$$(grep -l '^<!-- STUB -->' $(PAGES) 2>/dev/null | wc -l | tr -d ' ')"
	@printf "Stub-link refs:   %s unique\n" "$$(grep -hoE '\*\(stub[^)]*\)\*' *.md 2>/dev/null | wc -l | tr -d ' ')"
	@printf "Broken links:     %s file(s)\n" "$$(make --silent missing 2>/dev/null | wc -l | tr -d ' ')"

missing: ## List link targets that don't exist on disk (should be empty since all stubs are created)
	@for link in $$(grep -hoE '\(\./[a-zA-Z-]+\.md\)' *.md 2>/dev/null | sed 's|(\./||;s|)||' | sort -u); do \
		[ -f "$$link" ] || echo "$$link"; \
	done

stubs: ## List stub pages (header-only, body TBD)
	@grep -l '^<!-- STUB -->' $(PAGES) 2>/dev/null | sort

links: ## List all internal page links (deduped)
	@grep -hoE '\(\./[a-zA-Z-]+\.md[^)]*\)' *.md 2>/dev/null | sort -u

check: ## Verify all internal links resolve to written pages or to the INDEX forthcoming-nodes table
	@planned=$$(awk '/^## Forthcoming nodes/{flag=1; next} /^## /{flag=0} flag' INDEX.md \
		| grep -oE '\(\./[a-zA-Z-]+\.md\)' \
		| sed 's|(\./||;s|)||' | sort -u); \
	bad=0; \
	for f in $(PAGES) README.md STRUCTURE.md INDEX.md; do \
		[ -f "$$f" ] || continue; \
		for target in $$(grep -oE '\(\./[a-zA-Z-]+\.md\)' "$$f" | sed 's|(\./||;s|)||' | sort -u); do \
			if [ ! -f "$$target" ]; then \
				if ! echo "$$planned" | grep -qx "$$target"; then \
					printf "BROKEN: %s -> %s  (404 and not listed in INDEX forthcoming-nodes)\n" "$$f" "$$target"; \
					bad=1; \
				fi; \
			fi; \
		done; \
	done; \
	if [ "$$bad" = "0" ]; then echo "ok: all internal links resolve to written or canonically-planned pages."; fi; \
	exit $$bad
