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

MCPCONFORMANCE_TASKS_V2_PATH    ?= $(abspath $(REPO_ROOT)/../conf-template)
MCPCONFORMANCE_MRTR_PATH        ?= $(abspath $(REPO_ROOT)/../conf-template)
MCPCONFORMANCE_FILE_INPUTS_PATH ?= $(abspath $(REPO_ROOT)/../conf-pending)
MCPCONFORMANCE_LIST_TTL_PATH    ?= $(abspath $(REPO_ROOT)/../conf-pending)
MCPCONFORMANCE_AUTH_PATH        ?= $(abspath $(REPO_ROOT)/../conf-pending)

.PHONY: test testconfall testconf testconfauth \
        testconf-tasks testconf-tasks-v2 testconf-mrtr \
        testconf-list-ttl testconf-file-inputs testconf-auth-server \
        testconf-elicitation help

# Default umbrella — runs every conformance suite. Fork-based suites
# require their MCPCONFORMANCE_*_PATH worktree to exist (each target
# fail-fasts with a remediation message if missing).
test: testconf testconfauth testconf-tasks testconf-tasks-v2 testconf-mrtr testconf-list-ttl testconf-file-inputs testconf-auth-server ## Run all conformance suites (needs fork worktrees for SEP scenarios)

testconfall: testconf testconfauth ## Run base + auth conformance only

testconf: ## Run MCP conformance test suite (requires Node.js/npx)
	cd $(REPO_ROOT) && bash scripts/conformance-test.sh

testconfauth: ## Run MCP Auth conformance suite (client-side, requires mcpkit/auth)
	cd $(REPO_ROOT) && bash scripts/conformance-auth-test.sh

testconf-tasks: ## Run MCP Tasks v1 conformance (builds + starts server, runs tests, tears down)
	@(cd $(REPO_ROOT)/examples/tasks && go build -o tasks .) && \
	$(REPO_ROOT)/examples/tasks/tasks -addr :18091 & PID=$$!; \
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
		npx vitest run src/scenarios/server/tasks/all-scenarios.test.ts)
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
		npx vitest run src/scenarios/server/mrtr/all-scenarios.test.ts)
	(cd $(CONFORMANCE_DIR) && npm install --silent && npx vitest run mrtr/)

testconf-list-ttl: ## Run SEP-2549 list-TTL conformance — fork-based scenarios (auto-spawns 3 fixtures)
	@if [ ! -d "$(MCPCONFORMANCE_LIST_TTL_PATH)" ]; then \
		echo "MCPCONFORMANCE_LIST_TTL_PATH=$(MCPCONFORMANCE_LIST_TTL_PATH) does not exist."; \
		echo "Clone https://github.com/panyam/mcpconformance there or set MCPCONFORMANCE_LIST_TTL_PATH=<path-to-clone>."; \
		echo "Default expects the 'pending' branch checked out at ../conf-pending."; \
		exit 1; \
	fi
	@(cd $(REPO_ROOT)/examples/list-ttl && go build -o list-ttl-demo .)
	(cd $(MCPCONFORMANCE_LIST_TTL_PATH) && npm install --silent && \
		LIST_TTL_POSITIVE_URL=http://localhost:18094/mcp \
		LIST_TTL_POSITIVE_CMD="$(REPO_ROOT)/examples/list-ttl/list-ttl-demo --serve --addr=:18094 --ttl=60" \
		LIST_TTL_ZERO_URL=http://localhost:18095/mcp \
		LIST_TTL_ZERO_CMD="$(REPO_ROOT)/examples/list-ttl/list-ttl-demo --serve --addr=:18095 --ttl=0" \
		LIST_TTL_UNSET_URL=http://localhost:18096/mcp \
		LIST_TTL_UNSET_CMD="$(REPO_ROOT)/examples/list-ttl/list-ttl-demo --serve --addr=:18096" \
		npx vitest run src/scenarios/server/list-ttl/list-ttl.test.ts)

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

testconf-elicitation: ## Run elicitation conformance suite (SEP-1036 URL mode + form, requires Node.js, target server must be running)
	cd $(CONFORMANCE_DIR) && npm install --silent && \
	SERVER_URL=$${SERVER_URL:-http://localhost:8080/mcp} \
	npx tsx --test elicitation/scenarios.test.ts

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-30s\033[0m %s\n", $$1, $$2}'
