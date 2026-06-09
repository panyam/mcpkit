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

.PHONY: test testconf testconfauth \
        testconf-tasks testconf-tasks-v2 testconf-mrtr \
        testconf-file-inputs testconf-auth-server \
        testconf-stateless testconf-skills \
        testconf-elicitation testconf-upstream-audit \
        refresh-conformance check-conformance-stale \
        sync-conformance check-conformance-sync help

# Default umbrella — runs every conformance suite. Fork-based suites and the
# upstream-main audit each require their MCPCONFORMANCE_*_PATH worktree to
# exist (each target fail-fasts with a remediation message if missing). Fair
# game: contributors should have all conf-* sibling clones before pushing
# anything that touches conformance surfaces.
test: testconf testconfauth testconf-tasks testconf-tasks-v2 testconf-mrtr testconf-file-inputs testconf-auth-server testconf-stateless testconf-skills testconf-upstream-audit refresh-conformance ## Run all conformance suites (needs fork worktrees + conf-upstream-main)

testconf: ## Run MCP conformance test suite (requires Node.js/npx)
	cd $(REPO_ROOT) && bash scripts/conformance-test.sh

testconfauth: ## Run MCP Auth conformance suite (client-side, requires mcpkit/auth)
	cd $(REPO_ROOT) && bash scripts/conformance-auth-test.sh

testconf-tasks: ## Run MCP Tasks v1 conformance (builds + starts server, runs tests, tears down)
	@(cd $(REPO_ROOT)/examples/tasks && go build -o tasks .) && \
	$(REPO_ROOT)/examples/tasks/tasks --serve -addr :18091 & PID=$$!; \
	sleep 1; \
	(cd $(CONFORMANCE_DIR) && npm install --silent && \
	SERVER_URL=http://localhost:18091/mcp npx tsx --test tasks/scenarios.test.ts); \
	RC=$$?; kill $$PID 2>/dev/null; wait $$PID 2>/dev/null; exit $$RC

testconf-tasks-v2: ## Run SEP-2663 tasks conformance — fork-based scenarios + mcpkit-local stricter sentinel
	@if [ ! -d "$(MCPCONFORMANCE_TASKS_V2_PATH)" ]; then \
		echo "MCPCONFORMANCE_TASKS_V2_PATH=$(MCPCONFORMANCE_TASKS_V2_PATH) does not exist."; \
		echo "Clone https://github.com/panyam/mcpconformance there or set MCPCONFORMANCE_TASKS_V2_PATH=<path-to-clone>."; \
		exit 1; \
	fi
	@(cd $(REPO_ROOT)/examples/tasks-v2 && go build -o tasks-v2 .)
	(cd $(MCPCONFORMANCE_TASKS_V2_PATH) && npm install --silent && \
		TASKS_SERVER_URL=http://localhost:18092/mcp \
		TASKS_SERVER_CMD="$(REPO_ROOT)/examples/tasks-v2/tasks-v2 --serve --addr :18092" \
		npx vitest run src/scenarios/server/all-scenarios.test.ts)
	(cd $(CONFORMANCE_DIR) && npm install --silent && npx vitest run tasks-v2/)

testconf-mrtr: ## Run SEP-2322 MRTR conformance — fork-based scenarios + mcpkit-local stricter sentinel
	@if [ ! -d "$(MCPCONFORMANCE_MRTR_PATH)" ]; then \
		echo "MCPCONFORMANCE_MRTR_PATH=$(MCPCONFORMANCE_MRTR_PATH) does not exist."; \
		echo "Clone https://github.com/panyam/mcpconformance there or set MCPCONFORMANCE_MRTR_PATH=<path-to-clone>."; \
		exit 1; \
	fi
	@(cd $(REPO_ROOT)/examples/mrtr && go build -o mrtr-demo .)
	(cd $(MCPCONFORMANCE_MRTR_PATH) && npm install --silent && \
		MRTR_SERVER_URL=http://localhost:18093/mcp \
		MRTR_SERVER_CMD="$(REPO_ROOT)/examples/mrtr/mrtr-demo --serve --addr :18093" \
		npx vitest run src/scenarios/server/negative-mrtr.test.ts)
	(cd $(CONFORMANCE_DIR) && npm install --silent && npx vitest run mrtr/)

testconf-stateless: ## Run SEP-2575 stateless conformance — drives examples/stateless via upstream's ServerStatelessScenario class
	@if [ ! -d "$(MCPCONFORMANCE_STATELESS_PATH)" ]; then \
		echo "MCPCONFORMANCE_STATELESS_PATH=$(MCPCONFORMANCE_STATELESS_PATH) does not exist."; \
		echo "Clone https://github.com/modelcontextprotocol/conformance there:"; \
		echo "  git clone https://github.com/modelcontextprotocol/conformance.git $(MCPCONFORMANCE_STATELESS_PATH)"; \
		echo "Or set MCPCONFORMANCE_STATELESS_PATH=<path-to-clone>."; \
		exit 1; \
	fi
	@(cd $(REPO_ROOT)/examples/stateless && go build -o stateless-demo .)
	@OUT=$$(mktemp -d -t conf-stateless.XXXXXX); \
	echo "Spawning fixture on :18100, scratch dir $$OUT"; \
	$(REPO_ROOT)/examples/stateless/stateless-demo --serve --addr=:18100 > $$OUT/server.log 2>&1 & \
	PID=$$!; \
	for i in 1 2 3 4 5 6 7 8 9 10; do \
		curl -sf -X POST -H "Content-Type: application/json" \
			-d '{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"DRAFT-2026-v1","io.modelcontextprotocol/clientInfo":{"name":"poll","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}' \
			-H "MCP-Protocol-Version: DRAFT-2026-v1" \
			http://localhost:18100/mcp > /dev/null 2>&1 && break; \
		sleep 0.3; \
	done; \
	(cd $(MCPCONFORMANCE_STATELESS_PATH) && \
		node dist/index.js server \
			--url http://localhost:18100/mcp \
			--scenario server-stateless \
			-o $$OUT/checks > $$OUT/runner.log 2>&1); \
	RC=$$?; \
	kill $$PID 2>/dev/null; \
	wait $$PID 2>/dev/null; \
	CHECKS=$$(ls -t $$OUT/checks/server-server-stateless-*/checks.json 2>/dev/null | head -1); \
	if [ -z "$$CHECKS" ] || [ ! -f "$$CHECKS" ]; then \
		echo "testconf-stateless: upstream runner produced no checks.json"; \
		tail -30 $$OUT/runner.log; \
		exit 1; \
	fi; \
	FAILS=$$(grep -c '"status": "FAILURE"' $$CHECKS); \
	PASSES=$$(grep -c '"status": "SUCCESS"' $$CHECKS); \
	SKIPS=$$(grep -c '"status": "SKIPPED"' $$CHECKS); \
	WARNS=$$(grep -c '"status": "WARNING"' $$CHECKS); \
	echo "testconf-stateless: $$PASSES pass / $$FAILS fail / $$WARNS warn / $$SKIPS skip (artifact: $$CHECKS)"; \
	UNEXPECTED=$$(grep -B 2 '"status": "FAILURE"' $$CHECKS | grep '"name"' | grep -v "ServerRejectsUndeclaredCapability" | wc -l | tr -d ' '); \
	if [ "$$UNEXPECTED" -gt 0 ]; then \
		echo "Unexpected FAILURE rows:"; \
		grep -B 2 '"status": "FAILURE"' $$CHECKS | head -60; \
		exit 1; \
	fi; \
	if [ "$$FAILS" -gt 1 ]; then \
		echo "More FAILURE rows than the known upstream-test bug (-32003 array-vs-object); aborting"; \
		grep -B 2 '"status": "FAILURE"' $$CHECKS | head -60; \
		exit 1; \
	fi
# Known upstream test bug: ServerRejectsUndeclaredCapability checks for
# requiredCapabilities as a string-array but the SEP-2575 schema says
# ClientCapabilities object. mcpkit follows the schema; the test will
# align in a follow-up upstream PR. Until then this target tolerates
# exactly one FAILURE row and verifies it's that specific check.

testconf-file-inputs: ## Run SEP-2356 file-inputs conformance — fork-based scenarios (auto-spawns fixture)
	@if [ ! -d "$(MCPCONFORMANCE_FILE_INPUTS_PATH)" ]; then \
		echo "MCPCONFORMANCE_FILE_INPUTS_PATH=$(MCPCONFORMANCE_FILE_INPUTS_PATH) does not exist."; \
		echo "Clone https://github.com/panyam/mcpconformance there or set MCPCONFORMANCE_FILE_INPUTS_PATH=<path-to-clone>."; \
		echo "Default expects the 'pending' branch checked out at ../conf-pending."; \
		exit 1; \
	fi
	@(cd $(REPO_ROOT)/examples/file-inputs && go build -o file-inputs-demo .)
	(cd $(MCPCONFORMANCE_FILE_INPUTS_PATH) && npm install --silent && \
		FILE_INPUTS_SERVER_URL=http://localhost:18097/mcp \
		FILE_INPUTS_SERVER_CMD="$(REPO_ROOT)/examples/file-inputs/file-inputs-demo --serve --addr=:18097" \
		npx vitest run src/scenarios/server/file-inputs/file-inputs.test.ts)

testconf-auth-server: ## Run server-side auth conformance — fork-based scenarios (RFC 9728 PRM + RFC 8414 AS metadata + RFC 6750 Bearer enforcement)
	@if [ ! -d "$(MCPCONFORMANCE_AUTH_PATH)" ]; then \
		echo "MCPCONFORMANCE_AUTH_PATH=$(MCPCONFORMANCE_AUTH_PATH) does not exist."; \
		echo "Clone https://github.com/panyam/mcpconformance there or set MCPCONFORMANCE_AUTH_PATH=<path-to-clone>."; \
		echo "Default expects the 'pending' branch checked out at ../conf-pending."; \
		exit 1; \
	fi
	@(cd $(REPO_ROOT)/examples/auth && go build -o auth-demo .)
	@(cd $(MCPCONFORMANCE_AUTH_PATH) && npm install --silent)
	# Manual spawn: Phase 2 + 2.5's token-needing checks pull four pre-
	# minted tokens from the fixture's bootstrap (one valid, three
	# deliberately-bad-claim variants), so we spawn here (instead of
	# letting vitest auto-spawn) and curl /demo/bootstrap before
	# invoking the scenarios.
	@$(REPO_ROOT)/examples/auth/auth-demo --serve --addr=:18098 > /tmp/auth-demo.log 2>&1 & \
	PID=$$!; \
	for i in 1 2 3 4 5 6 7 8 9 10; do \
	  curl -sf http://localhost:18098/.well-known/oauth-protected-resource > /dev/null 2>&1 && break; \
	  sleep 0.5; \
	done; \
	BOOT=$$(curl -sf http://localhost:18098/demo/bootstrap); \
	TOK_VALID=$$(echo "$$BOOT" | python3 -c 'import json,sys;print(json.load(sys.stdin)["tok_read"])' 2>/dev/null); \
	TOK_RW=$$(echo "$$BOOT" | python3 -c 'import json,sys;print(json.load(sys.stdin)["tok_read_write"])' 2>/dev/null); \
	TOK_FULL=$$(echo "$$BOOT" | python3 -c 'import json,sys;print(json.load(sys.stdin)["tok_all"])' 2>/dev/null); \
	TOK_EXPIRED=$$(echo "$$BOOT" | python3 -c 'import json,sys;print(json.load(sys.stdin)["tok_expired"])' 2>/dev/null); \
	TOK_WRONG_AUD=$$(echo "$$BOOT" | python3 -c 'import json,sys;print(json.load(sys.stdin)["tok_wrong_audience"])' 2>/dev/null); \
	TOK_WRONG_ISS=$$(echo "$$BOOT" | python3 -c 'import json,sys;print(json.load(sys.stdin)["tok_wrong_issuer"])' 2>/dev/null); \
	TOK_UPSTREAM=$$(echo "$$BOOT" | python3 -c 'import json,sys;print(json.load(sys.stdin)["tok_upstream_assertion"])' 2>/dev/null); \
	AS_TOKEN_ENDPOINT=$$(curl -sf http://localhost:18098/.well-known/oauth-authorization-server | python3 -c 'import json,sys;print(json.load(sys.stdin)["token_endpoint"])' 2>/dev/null); \
	(cd $(MCPCONFORMANCE_AUTH_PATH) && \
	  AUTH_SERVER_URL=http://localhost:18098/mcp \
	  AUTH_VALID_TOKEN="$$TOK_VALID" \
	  AUTH_READWRITE_TOKEN="$$TOK_RW" \
	  AUTH_FULL_TOKEN="$$TOK_FULL" \
	  AUTH_EXPIRED_TOKEN="$$TOK_EXPIRED" \
	  AUTH_WRONG_AUDIENCE_TOKEN="$$TOK_WRONG_AUD" \
	  AUTH_WRONG_ISSUER_TOKEN="$$TOK_WRONG_ISS" \
	  AUTH_SUBJECT_ASSERTION_TOKEN="$$TOK_UPSTREAM" \
	  AUTH_AS_TOKEN_ENDPOINT="$$AS_TOKEN_ENDPOINT" \
	  npx vitest run src/scenarios/server/auth/auth.test.ts); \
	RC=$$?; \
	kill $$PID 2>/dev/null; \
	wait $$PID 2>/dev/null; \
	exit $$RC

testconf-skills: ## Run SEP-2640 skills conformance — drives examples/skills via the fork's scenario runner. Scenarios land in a follow-up using the Scenario class primitives PR 8 introduced; this target wires the fixture spawn + URL today so the conf-side PR plugs in cleanly.
	@if [ ! -d "$(MCPCONFORMANCE_SKILLS_PATH)" ]; then \
		echo "MCPCONFORMANCE_SKILLS_PATH=$(MCPCONFORMANCE_SKILLS_PATH) does not exist."; \
		echo "Clone https://github.com/panyam/mcpconformance there or set MCPCONFORMANCE_SKILLS_PATH=<path-to-clone>."; \
		echo "Default expects the main branch (PR 330) checked out at ../conf-main."; \
		exit 1; \
	fi
	@(cd $(REPO_ROOT)/examples/skills && go build -o skills-demo .)
	@OUT=$$(mktemp -d -t conf-skills.XXXXXX); \
	echo "Spawning fixture on :18099, scratch dir $$OUT"; \
	$(REPO_ROOT)/examples/skills/skills-demo --serve --addr=:18099 --skills=$(REPO_ROOT)/examples/skills/skills > $$OUT/server.log 2>&1 & \
	PID=$$!; \
	for i in 1 2 3 4 5 6 7 8 9 10; do \
		curl -sf -o /dev/null -X OPTIONS http://localhost:18099/mcp && break; \
		sleep 0.3; \
	done; \
	(cd $(MCPCONFORMANCE_SKILLS_PATH) && \
		node dist/index.js server \
			--url http://localhost:18099/mcp \
			--scenario sep-2640-skills \
			-o $$OUT/checks > $$OUT/runner.log 2>&1); \
	RC=$$?; \
	kill $$PID 2>/dev/null; \
	wait $$PID 2>/dev/null; \
	if [ $$RC -ne 0 ]; then \
		echo "testconf-skills: runner exited $$RC (artifacts in $$OUT)"; \
		tail -30 $$OUT/runner.log 2>/dev/null; \
		exit $$RC; \
	fi
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

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-30s\033[0m %s\n", $$1, $$2}'
