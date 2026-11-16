# examples/common/playground.mk
#
# Shared Makefile fragment for browser-ready examples (#1719). An example
# with web/playground.json includes it:
#
#   include ../common/playground.mk
#
# serve-web builds the example for wasm and serves the shared playground.
# The package comes from the manifest, so the recipe is the same everywhere.

PLAYGROUND_PORT ?= 8788
PLAYGROUND_MK_DIR := $(dir $(lastword $(MAKEFILE_LIST)))

serve-web: ## Serve this example's browser playground on http://localhost:8788/
	@bash $(PLAYGROUND_MK_DIR)../../scripts/serve-example-wasm.sh . $(PLAYGROUND_PORT)

.PHONY: serve-web
