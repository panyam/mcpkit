# Conformance test orchestration.
#
# The top-level repo Makefile delegates to these targets via
# `make -C conformance ...`. Targets are also runnable directly from
# this directory.
#
# Per-suite MCPCONFORMANCE_*_PATH vars point at worktrees of the
# panyam/mcpconformance fork because different SEPs live on different
# branches while their upstream PRs are still draft. Override per-
# invocation when scenarios move (e.g., a SEP splits from `pending`
# into its own feat branch waiting upstream approval):
#
#   MCPCONFORMANCE_FILE_INPUTS_PATH=$(realpath ../conf-sep-2356) \
#     make testconf-file-inputs
#
# Defaults resolve relative to this Makefile's directory so absolute
# paths don't leak into committed code.

CONFORMANCE_DIR := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))
REPO_ROOT       := $(abspath $(CONFORMANCE_DIR)/..)

# Per-suite MCPCONFORMANCE_*_PATH defaults are generated from
# conformance/local-suites.yaml. After editing the YAML's source.default_path
# for any suite, regenerate with:
#   uv run scripts/gen_conf_paths.py --write
# CI's check-local-suites-stale rejects drift (case E).
include $(CONFORMANCE_DIR)/path-defaults.mk
# Audit harness: points at a direct clone of modelcontextprotocol/conformance@main
# (the real upstream, not our panyam fork). Used by testconf-upstream-audit to
# grade mcpkit against every scenario upstream currently ships.
MCPCONFORMANCE_BASE_PATH        ?= $(abspath $(REPO_ROOT)/../conf-upstream-main)

.PHONY: test testconf testconfauth testconf-client \
        testconf-tasks testconf-tasks-v2 testconf-mrtr \
        testconf-file-inputs testconf-auth-server \
        testconf-stateless testconf-skills \
        testconf-elicitation testconf-upstream-audit \
        testconf-external-checker \
        refresh-conformance check-conformance-stale \
        sync-conformance check-conformance-sync help

# Default umbrella — runs every conformance suite. Fork-based suites and the
# upstream-main audit each require their MCPCONFORMANCE_*_PATH worktree to
# exist (each target fail-fasts with a remediation message if missing). Fair
# game: contributors should have all conf-* sibling clones before pushing
# anything that touches conformance surfaces.
test: testconf testconfauth testconf-client testconf-tasks testconf-tasks-v2 testconf-mrtr testconf-file-inputs testconf-auth-server testconf-stateless testconf-skills testconf-upstream-audit refresh-conformance ## Run all conformance suites (needs fork worktrees + conf-upstream-main)

testconf: ## Run MCP conformance test suite (requires Node.js/npx)
	cd $(REPO_ROOT) && bash scripts/conformance-test.sh

testconfauth: ## Run MCP Auth conformance suite (client-side, requires mcpkit/auth)
	cd $(REPO_ROOT) && bash scripts/conformance-auth-test.sh

testconf-client: ## Run full client conformance suite — core + auth + backcompat + extensions (auto-builds testclient)
	@bash $(CONFORMANCE_DIR)/scripts/conf-client.sh

testconf-tasks: ## Run MCP Tasks v1 conformance (builds + starts server, runs tests, tears down)
	@bash $(CONFORMANCE_DIR)/scripts/conf-tasks.sh

testconf-tasks-v2: ## Run SEP-2663 tasks conformance — upstream scenarios + mcpkit-local stricter sentinel
	@bash $(CONFORMANCE_DIR)/scripts/conf-tasks-v2.sh

testconf-mrtr: ## Run SEP-2322 MRTR conformance — upstream scenarios + mcpkit-local stricter sentinel
	@bash $(CONFORMANCE_DIR)/scripts/conf-mrtr.sh

testconf-stateless: ## Run SEP-2575 stateless conformance — drives examples/stateless via upstream's ServerStatelessScenario class
	@bash $(CONFORMANCE_DIR)/scripts/conf-stateless.sh
# Suite is fully green (30/30) as of modelcontextprotocol/conformance
# 0.2.0-alpha.9. Upstream #376 fixed the former ServerRejectsUndeclaredCapability
# bug (it had checked requiredCapabilities as a string-array; the SEP-2575
# schema says ClientCapabilities object, which mcpkit follows). The target now
# requires zero FAILURE rows — if upstream regresses, we want to catch it.

testconf-file-inputs: ## Run SEP-2356 file-inputs conformance — fork-based scenarios (auto-spawns fixture)
	@bash $(CONFORMANCE_DIR)/scripts/conf-file-inputs.sh

testconf-auth-server: ## Run server-side auth conformance — fork-based scenarios (RFC 9728 PRM + RFC 8414 AS metadata + RFC 6750 Bearer enforcement)
	@bash $(CONFORMANCE_DIR)/scripts/conf-auth-server.sh

testconf-skills: ## Run SEP-2640 skills conformance — informational (exit 0 regardless of check results while WG iterates sep-2640.yaml). Drives examples/skills via the fork's scenario runner. Scenarios land in a follow-up using the Scenario class primitives PR 8 introduced; this target wires the fixture spawn + URL today so the conf-side PR plugs in cleanly.
	@bash $(CONFORMANCE_DIR)/scripts/conf-skills.sh
# Scenario class for SEP-2640 to be registered under the name
# "sep-2640-skills" in the fork's src/scenarios/index.ts, mirroring how
# tasks-v2 and stateless are wired. Per Paul Carleton's review, the
# scenarios use ctx.connect() + Connection.request() primitives instead
# of a per-SEP all-scenarios.test.ts file. Tracking: mcpkit#567.

testconf-elicitation: ## Run elicitation conformance suite (SEP-1036 URL mode + form, requires Node.js, target server must be running)
	cd $(CONFORMANCE_DIR) && npm install --silent && \
	SERVER_URL=$${SERVER_URL:-http://localhost:8080/mcp} \
	npx tsx --test elicitation/scenarios.test.ts

refresh-conformance: ## Regenerate CONFORMANCE.md from upstream tier-check + traceability (issue #498). Output: CONFORMANCE.md at repo root.
	@if [ ! -d "$(MCPCONFORMANCE_BASE_PATH)" ]; then \
		echo "MCPCONFORMANCE_BASE_PATH=$(MCPCONFORMANCE_BASE_PATH) does not exist."; \
		echo "Clone https://github.com/modelcontextprotocol/conformance there:"; \
		echo "  git clone https://github.com/modelcontextprotocol/conformance.git $(MCPCONFORMANCE_BASE_PATH)"; \
		echo "Or set MCPCONFORMANCE_BASE_PATH=<path-to-clone>."; \
		exit 1; \
	fi
	cd $(REPO_ROOT) && MCPCONFORMANCE_BASE_PATH="$(MCPCONFORMANCE_BASE_PATH)" bash scripts/refresh-conformance.sh

sync-conformance: ## Fetch all conformance worktrees + validate (run before `make -C conformance test`). Catches stale Makefile paths, dirty trees, behind-origin branches.
	cd $(REPO_ROOT) && uv run scripts/sync_conformance_worktrees.py --fetch

check-conformance-sync: ## Validate conformance worktrees without fetching (preflight gate; faster than sync-conformance).
	cd $(REPO_ROOT) && uv run scripts/sync_conformance_worktrees.py

check-conformance-stale: refresh-conformance ## CI gate — fail if CONFORMANCE.md is stale (issue 498). Compares working tree to HEAD so the gate is robust to staging order.
	@cd $(REPO_ROOT) && git diff --exit-code HEAD -- CONFORMANCE.md > /dev/null 2>&1 || { \
		echo "CONFORMANCE.md is stale relative to current testserver + upstream conformance."; \
		echo "Run 'make refresh-conformance' locally and commit the regenerated file."; \
		echo ""; \
		git diff HEAD -- CONFORMANCE.md | head -60; \
		exit 1; \
	}
	@echo "CONFORMANCE.md is up to date."

testconf-upstream-audit: ## Audit mcpkit against modelcontextprotocol/conformance@main (informational — exit 0 regardless of scenario pass/fail). Output: conformance/UPSTREAM_AUDIT.md.
	@if [ ! -d "$(MCPCONFORMANCE_BASE_PATH)" ]; then \
		echo "MCPCONFORMANCE_BASE_PATH=$(MCPCONFORMANCE_BASE_PATH) does not exist."; \
		echo "Clone https://github.com/modelcontextprotocol/conformance there:"; \
		echo "  git clone https://github.com/modelcontextprotocol/conformance.git $(MCPCONFORMANCE_BASE_PATH)"; \
		echo "Or set MCPCONFORMANCE_BASE_PATH=<path-to-clone>."; \
		exit 1; \
	fi
	cd $(REPO_ROOT) && MCPCONFORMANCE_BASE_PATH="$(MCPCONFORMANCE_BASE_PATH)" bash scripts/conformance-audit.sh

# External, third-party CLIENT-side gauntlet. Hits a live val.town endpoint that
# grades the mcpkit client against the integrated stateless draft wire
# (2026-07-28 / DRAFT-2026-v1). Deliberately NOT in the `test` umbrella above and
# NOT a CI gate — the endpoint is external + ephemeral. Run manually; the report
# is a point-in-time snapshot like UPSTREAM_AUDIT.md. Override the target with
# EXTERNAL_CHECKER_URL=<url> when the dated endpoint rotates.
testconf-external-checker: ## Grade the mcpkit CLIENT against the external stateless-draft checker (live network). Output: conformance/EXTERNAL_CHECKER.md.
	cd $(REPO_ROOT) && go run ./cmd/external-checker $(if $(EXTERNAL_CHECKER_URL),-url $(EXTERNAL_CHECKER_URL),)

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-30s\033[0m %s\n", $$1, $$2}'
