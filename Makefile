# MCPKit Makefile

# Sub-modules that get tagged alongside the root module. Every importable
# sub-module (its own go.mod, `require`s the root) needs a tag here so
# downstream can `go get <module>@vX.Y.Z` — `replace` directives are ignored
# by non-main modules. ext/tasks, ext/skills, stores/redis, and the
# experimental events modules were added once they shipped their own go.mod.
SUB_MODS_TO_TAG := \
	agent agent/host agent/store/redis \
	ext/auth ext/otel ext/ui ext/tasks ext/skills \
	stores/redis \
	experimental/ext/events \
	experimental/ext/events/stores/memory experimental/ext/events/stores/gorm experimental/ext/events/stores/redis \
	experimental/ext/events/clients/go \
	cmd/testclient cmd/common cmd/mcpskills cmd/agentchat \
	examples/mcpskills-walkthrough \
	tests/e2e tests/keycloak

# Conformance test orchestration lives in `conformance/Makefile`.
# Per-suite MCPCONFORMANCE_*_PATH vars are documented there.
MCPKIT_DIR := $(abspath $(dir $(firstword $(MAKEFILE_LIST))))

# =============================================================================
# Build & test
# =============================================================================

build: ## Build all packages
	go build ./...

test: ## Run unit tests
	go test ./... -count=1 -timeout 30s

test-race: ## Run unit tests with race detector
	go test -race ./... -count=1 -timeout 60s

test-v: ## Run unit tests with verbose output
	go test ./... -count=1 -timeout 30s -v

cover: ## Run tests with coverage summary (root module only)
	go test -cover ./... -count=1 -timeout 30s

cover-html: ## Run tests with coverage and generate HTML report (root module only)
	@mkdir -p $(REPORT_DIR)
	go test -coverprofile=$(REPORT_DIR)/coverage.out ./... -count=1 -timeout 120s
	go tool cover -html=$(REPORT_DIR)/coverage.out -o $(REPORT_DIR)/coverage.html
	@echo "Coverage report: $(REPORT_DIR)/coverage.html"

cover-func: ## Show per-function coverage sorted by lowest (root module only)
	@mkdir -p $(REPORT_DIR)
	go test -coverprofile=$(REPORT_DIR)/coverage.out ./... -count=1 -timeout 30s
	go tool cover -func=$(REPORT_DIR)/coverage.out | sort -k3 -n | head -30

cover-all: ## Run coverage across root + all sub-modules, generate per-module HTML reports
	@mkdir -p $(REPORT_DIR)
	@echo "==> coverage: root module"
	@go test -coverprofile=$(REPORT_DIR)/coverage-root.out ./... -count=1 -timeout 30s
	@go tool cover -html=$(REPORT_DIR)/coverage-root.out -o $(REPORT_DIR)/coverage-root.html
	@for mod in ext/auth ext/ui; do \
		echo "==> coverage: $$mod"; \
		(cd $$mod && go test -coverprofile=../../$(REPORT_DIR)/coverage-$$(echo $$mod | tr / -).out ./... -count=1 -timeout 30s) || true; \
		go tool cover -html=$(REPORT_DIR)/coverage-$$(echo $$mod | tr / -).out -o $(REPORT_DIR)/coverage-$$(echo $$mod | tr / -).html 2>/dev/null || true; \
	done
	@echo ""
	@echo "Coverage reports:"
	@ls -1 $(REPORT_DIR)/coverage-*.html 2>/dev/null

smoke: ## Run smoke tests (starts test servers, tests both transports via curl)
	bash scripts/smoke-test.sh

smoke-wire: ## Boot each --wire example and assert wire selection took effect (issue 824)
	bash scripts/smoke-wire.sh

verify-dual: ## Run each auto-drivable example walkthrough on both wires; assert behavioral parity (issue 478)
	bash scripts/verify-dual.sh

# Conformance shims — actual logic lives in conformance/Makefile.

testconfall: ## Run base + auth conformance only (delegates to conformance/Makefile)
	$(MAKE) -C conformance test

testconf: ## Run MCP conformance test suite (delegates to conformance/Makefile)
	$(MAKE) -C conformance testconf

testconfauth: ## Run MCP Auth conformance suite (delegates to conformance/Makefile)
	$(MAKE) -C conformance testconfauth

testconf-tasks: ## Run MCP Tasks v1 conformance (delegates to conformance/Makefile)
	$(MAKE) -C conformance testconf-tasks

testconf-tasks-v2: ## Run SEP-2663 tasks conformance — upstream + mcpkit-local sentinel (delegates to conformance/Makefile)
	$(MAKE) -C conformance testconf-tasks-v2

testconf-mrtr: ## Run SEP-2322 MRTR conformance — upstream + mcpkit-local sentinel (delegates to conformance/Makefile)
	$(MAKE) -C conformance testconf-mrtr

testconf-file-inputs: ## Run SEP-2356 file-inputs conformance — fork-based (delegates to conformance/Makefile)
	$(MAKE) -C conformance testconf-file-inputs

testconf-auth-server: ## Run server-side auth conformance — fork-based, RFC 9728 + RFC 8414 (delegates to conformance/Makefile)
	$(MAKE) -C conformance testconf-auth-server

testconf-elicitation: ## Run SEP-1036 elicitation conformance (delegates to conformance/Makefile)
	$(MAKE) -C conformance testconf-elicitation

testconf-skills: ## Run SEP-2640 skills conformance — fork-based (delegates to conformance/Makefile)
	$(MAKE) -C conformance testconf-skills

testconf-external-checker: ## Grade the mcpkit CLIENT against the external stateless-draft checker (live network, not a CI gate; delegates to conformance/Makefile)
	$(MAKE) -C conformance testconf-external-checker

refresh-conformance: ## Regenerate CONFORMANCE.md from upstream tier-check + traceability (delegates to conformance/Makefile)
	$(MAKE) -C conformance refresh-conformance

check-conformance-stale: check-local-suites-stale ## Fail if CONFORMANCE.md is stale relative to current testserver + upstream (CI gate)
	$(MAKE) -C conformance check-conformance-stale

check-local-suites-stale: ## CI gate — fail if conformance/local-suites.yaml drifts from the Makefile (cases A/B/C)
	uv run scripts/check_local_suites.py

check-snippets: ## CI gate — fail if docs/GETTING_STARTED.md Go snippets drift from examples/getting-started/ (issue 853)
	go run ./tools/check-snippets

check-auth-markers: ## Fail if an AUTH_SPEC_COVERAGE.md clause lacks its inline ext/auth marker (issue 504)
	go run ./tools/check-auth-markers

refresh-apps-compat-report: ## Regenerate conformance/apps/COMPAT.md from umbrella tracking issue (#533). Uses gh CLI.
	./scripts/refresh-apps-compat-report.sh

check-apps-compat-stale: refresh-apps-compat-report ## Fail if conformance/apps/COMPAT.md is stale relative to umbrella #533 (CI gate)
	@git diff --exit-code conformance/apps/COMPAT.md || ( \
		echo "::error::conformance/apps/COMPAT.md is stale."; \
		echo "::error::Run 'make refresh-apps-compat-report' locally and commit the diff."; \
		exit 1 \
	)

test-agent: ## Run agent sub-module tests
	cd agent && go test ./... -count=1 -timeout 30s
	cd agent/store/redis && go test ./... -count=1 -timeout 60s
	cd agent/host && go test ./... -count=1 -timeout 60s
	cd cmd/agentchat && go test ./... -count=1 -timeout 60s
	cd examples/agent-async && go test ./... -count=1 -timeout 60s
	cd examples/skills && go test ./... -count=1 -timeout 60s -run TestAgentScenario
	cd examples/tasks-v2 && go test ./... -count=1 -timeout 60s -run TestAgentScenario

test-auth: ## Run auth sub-module tests
	cd ext/auth && go test ./... -count=1 -timeout 30s

test-otel: ## Run SEP-414 ext/otel adapter sub-module tests
	cd ext/otel && go test ./... -count=1 -timeout 30s

test-otel-example: ## Run the examples/otel/stdout smoke test
	cd examples/otel/stdout && go test ./... -count=1 -timeout 30s

test-ui: ## Run UI extension sub-module tests
	cd ext/ui && $(MAKE) test

test-skills: ## Run skills extension sub-module tests (SEP-2640, experimental)
	cd ext/skills && go test ./... -count=1 -timeout 30s

test-mcpskills: ## Run cmd/mcpskills CLI smoke tests (SEP-2640)
	cd cmd/mcpskills && go test ./... -count=1 -timeout 60s

build-mcpskills: ## Build the mcpskills CLI binary into ./bin/mcpskills
	@mkdir -p bin
	cd cmd/mcpskills && go build -o ../../bin/mcpskills .
	@echo "wrote bin/mcpskills"

test-mcpskills-walkthrough: ## Run the mcpskills CLI walkthrough non-interactively as a CI smoke test
	cd examples/mcpskills-walkthrough && go run . --non-interactive

build-bridge: ## Compile mcp-app-bridge.ts → .js (delegates to ext/ui)
	cd ext/ui && $(MAKE) build-bridge

test-protogen: ## Run protogen sub-module tests + e2e example
	cd experimental/ext/protogen && go test ./... -count=1 -timeout 30s && $(MAKE) test-e2e

test-e2e: ## Run all E2E tests (auth, apps — no Docker)
	cd tests/e2e && go test ./... -count=1 -timeout 60s

test-experimental: ## Run all experimental POC tests (delegates to experimental/Makefile)
	$(MAKE) -C experimental test

test-experimental-events: ## Run experimental ext/events library tests
	$(MAKE) -C experimental test-events

test-experimental-events-clients-go: ## Run experimental ext/events Go client SDK tests
	$(MAKE) -C experimental test-events-clients-go

test-experimental-events-stores-gorm: ## Run experimental ext/events GORM stores (sqlite + inmemory; no Docker required)
	$(MAKE) -C experimental test-events-stores-gorm

test-experimental-events-stores-gorm-pg: ## Run experimental ext/events GORM stores against a real Postgres container (Docker)
	$(MAKE) -C experimental test-events-stores-gorm-pg

test-experimental-events-stores-redis: ## Run experimental ext/events Redis pubsub Emitter (miniredis; no Docker required)
	$(MAKE) -C experimental test-events-stores-redis

test-experimental-events-stores-redis-real: ## Run experimental ext/events Redis pubsub Emitter against a real Redis container (Docker)
	$(MAKE) -C experimental test-events-stores-redis-real

test-experimental-events-discord: ## Run experimental events Discord example tests
	$(MAKE) -C experimental test-events-discord

test-experimental-events-telegram: ## Run experimental events Telegram example tests
	$(MAKE) -C experimental test-events-telegram

test-apps-playwright: ## Run ext-apps Playwright tests against testserver (needs Node.js + Playwright). EXAMPLE=<name> picks a fixture.
	uv run scripts/apps_playwright_test.py

test-apps-playwright-docker: ## Same as test-apps-playwright but inside upstream's playwright Docker image — CI-identical baselines
	uv run scripts/apps_playwright_test.py --docker

test-apps-playwright-all: ## Sweep every registered compat fixture sequentially. Exits non-zero if any fail.
	uv run scripts/apps_playwright_test.py --all

test-apps-playwright-docker-all: ## --all + --docker. The canonical visual gate across all 21 compat fixtures.
	uv run scripts/apps_playwright_test.py --docker --all

refresh-visual-gallery: ## Regenerate the side-by-side baselines gallery (mcpkit vs upstream PNGs with bordered-box diff regions). Manual; run after a successful docker-all + commit the regenerated artifacts.
	uv run scripts/apps_visual_gallery.py
	@echo "Gallery refreshed. Commit docs/site/content/conformance/apps/visual-gallery/ + docs/site/static/conformance/apps/visual-gallery/."

release-audit-apps: ## Release-time apps/compat audit umbrella — fully end-to-end: refresh ext-apps clone → docker-all (parity + visual gate) → regenerate gallery → commit + push the gallery → ghdeploy. Single command for "release-time, just do everything."
	@echo "==> [1/5] Refreshing upstream ext-apps clone at /tmp/ext-apps..."
	@if [ -f /tmp/ext-apps/.git/HEAD ]; then \
		(cd /tmp/ext-apps && git pull --quiet) && echo "  pulled"; \
	elif [ -e /tmp/ext-apps ]; then \
		rm -rf /tmp/ext-apps && git clone --quiet https://github.com/modelcontextprotocol/ext-apps.git /tmp/ext-apps && echo "  re-cloned (was corrupted: missing .git/HEAD)"; \
	else \
		git clone --quiet https://github.com/modelcontextprotocol/ext-apps.git /tmp/ext-apps && echo "  cloned"; \
	fi
	@echo ""
	@echo "==> [2/5] Running docker-all (parity diff + Playwright visual gate across 21 fixtures)..."
	@$(MAKE) test-apps-playwright-docker-all || echo "  WARNING: docker-all failed. Continuing so drift is captured in the gallery for inspection."
	@echo ""
	@echo "==> [3/5] Regenerating visual gallery..."
	@$(MAKE) refresh-visual-gallery
	@echo ""
	@echo "==> [4/5] Committing + pushing regenerated gallery artifacts..."
	@if git status --porcelain docs/site/content/conformance/apps/visual-gallery/ docs/site/static/conformance/apps/visual-gallery/ 2>/dev/null | grep -q .; then \
		git add docs/site/content/conformance/apps/visual-gallery/ docs/site/static/conformance/apps/visual-gallery/; \
		git commit -m "refresh: visual gallery for release"; \
		git push; \
		echo "  committed + pushed"; \
	else \
		echo "  no gallery changes; nothing to commit"; \
	fi
	@echo ""
	@echo "==> [5/5] Deploying docs site to gh-pages..."
	@$(MAKE) ghdeploy
	@echo ""
	@echo "==> Release audit complete."
	@echo "    Gallery: https://panyam.github.io/mcpkit/conformance/apps/visual-gallery/"
	@echo "    (gh-pages CDN may take 1-5 min to flush)"

demo-app: ## Browse a compat fixture interactively. Default: mcpkit-Go server + basic-host (no LLM needed). basic-host runs on :8080; open it manually (or pass OPEN=1 to auto-open). Override with RENDERER=mcpjam for wire inspection. Usage: make demo-app EXAMPLE=<name>.
	EXAMPLE=$(EXAMPLE) SERVER=$${SERVER:-go} RENDERER=$${RENDERER:-basic-host} OPEN=$(OPEN) uv run scripts/apps_demo.py

demo-upstream: ## Browse the upstream TS reference server instead of the Go fixture. Same axes as demo-app. Use this for SKIP examples (lazy-auth-server, video-resource-server, qr-server, say-server) that have no Go drop-in. Usage: make demo-upstream EXAMPLE=<name>.
	EXAMPLE=$(EXAMPLE) SERVER=upstream RENDERER=$${RENDERER:-basic-host} OPEN=$(OPEN) uv run scripts/apps_demo.py

testkcl: ## Run Keycloak auth interop tests (requires Docker, run upkcl first)
	cd tests/keycloak && go test ./... -count=1 -timeout 120s -v

REPORT_DIR := tests/reports

# run_stage runs a make target as a testall stage with per-stage log files.
# Each stage writes to $(REPORT_DIR)/stage-<label>.log (not the shared run.log).
# Usage: $(call run_stage,STEP_NUM,TOTAL,LABEL,MAKE_TARGET)
# Shell vars PASS, FAIL, STAGES must be initialized by the caller.
define run_stage
	STAGE_LOG=$(REPORT_DIR)/stage-$(3).log; \
	STAGE_START=$$(date +%s); \
	echo "--- [$(1)/$(2)] $(3) ---" | tee -a $(REPORT_DIR)/run.log; \
	echo "=== Stage $(1)/$(2): $(3) (make $(4)) ===" > $$STAGE_LOG; \
	echo "Started: $$(date)" >> $$STAGE_LOG; \
	if $(MAKE) $(4) >> $$STAGE_LOG 2>&1; then \
		ELAPSED=$$(($$(date +%s) - STAGE_START)); \
		echo "  PASS: $(3) ($${ELAPSED}s)" | tee -a $(REPORT_DIR)/run.log; \
		PASS=$$((PASS+1)); STAGES="$$STAGES $(3):PASS:$(4):$${ELAPSED}s"; \
	else \
		ELAPSED=$$(($$(date +%s) - STAGE_START)); \
		echo "  FAIL: $(3) ($${ELAPSED}s)" | tee -a $(REPORT_DIR)/run.log; \
		FAIL=$$((FAIL+1)); STAGES="$$STAGES $(3):FAIL:$(4):$${ELAPSED}s"; \
		echo "  --- $(3) tail ---"; tail -20 $$STAGE_LOG; echo "  ---"; \
	fi; \
	echo "Finished: $$(date) (elapsed $${ELAPSED}s)" >> $$STAGE_LOG;
endef

# run_stage_info is the soft-failure variant for experimental or in-flight
# conformance work where the suite SHOULD surface in testall reports but
# MUST NOT block the build. Failure is recorded as INFO and counted
# separately so a failing experimental stage does not show up in the
# PASS/FAIL tallies. Shell vars PASS, FAIL, INFO, STAGES must be
# initialized by the caller.
define run_stage_info
	STAGE_LOG=$(REPORT_DIR)/stage-$(3).log; \
	STAGE_START=$$(date +%s); \
	echo "--- [$(1)/$(2)] $(3) (informational) ---" | tee -a $(REPORT_DIR)/run.log; \
	echo "=== Stage $(1)/$(2): $(3) (make $(4)) [informational] ===" > $$STAGE_LOG; \
	echo "Started: $$(date)" >> $$STAGE_LOG; \
	if $(MAKE) $(4) >> $$STAGE_LOG 2>&1; then \
		ELAPSED=$$(($$(date +%s) - STAGE_START)); \
		echo "  PASS: $(3) ($${ELAPSED}s, informational)" | tee -a $(REPORT_DIR)/run.log; \
		PASS=$$((PASS+1)); STAGES="$$STAGES $(3):PASS:$(4):$${ELAPSED}s"; \
	else \
		ELAPSED=$$(($$(date +%s) - STAGE_START)); \
		echo "  INFO: $(3) ($${ELAPSED}s, informational, not counted as failure)" | tee -a $(REPORT_DIR)/run.log; \
		INFO=$$((INFO+1)); STAGES="$$STAGES $(3):INFO:$(4):$${ELAPSED}s"; \
		echo "  --- $(3) tail ---"; tail -20 $$STAGE_LOG; echo "  ---"; \
	fi; \
	echo "Finished: $$(date) (elapsed $${ELAPSED}s)" >> $$STAGE_LOG;
endef

testall: ## Run ALL tests (starts Keycloak if needed) + per-stage HTML reports
	@mkdir -p $(REPORT_DIR)
	@rm -f $(REPORT_DIR)/stage-*.log
	@echo "=== MCPKit Comprehensive Test Suite ===" | tee $(REPORT_DIR)/run.log
	@echo "Started: $$(date)" | tee -a $(REPORT_DIR)/run.log
	@PASS=0; FAIL=0; INFO=0; STAGES=""; \
	echo "" | tee -a $(REPORT_DIR)/run.log; \
	$(call run_stage,1,9,unit+coverage,cover-html) \
	$(call run_stage,2,9,race,test-race) \
	$(call run_stage,3,9,auth,test-auth) \
	$(call run_stage,4,9,ui,test-ui) \
	$(call run_stage,5,9,protogen,test-protogen) \
	$(call run_stage,5a,9,otel-adapter,test-otel) \
	$(call run_stage,5b,9,otel-example,test-otel-example) \
	$(call run_stage,6,9,e2e,test-e2e) \
	$(call run_stage,7a,9,experimental-events,test-experimental-events) \
	$(call run_stage,7b,9,experimental-events-clients-go,test-experimental-events-clients-go) \
	$(call run_stage,7c,9,experimental-events-stores-gorm,test-experimental-events-stores-gorm) \
	$(call run_stage,7d,9,experimental-events-stores-redis,test-experimental-events-stores-redis) \
	$(call run_stage,7e,9,experimental-events-discord,test-experimental-events-discord) \
	$(call run_stage,7f,9,experimental-events-telegram,test-experimental-events-telegram) \
	$(call run_stage,8a,9,conformance,testconf) \
	$(call run_stage,8b,9,auth-conformance,testconfauth) \
	$(call run_stage,8c,9,tasks-conformance,testconf-tasks) \
	$(call run_stage,8d,9,tasks-v2-conformance,testconf-tasks-v2) \
	$(call run_stage,8e,9,mrtr-conformance,testconf-mrtr) \
	$(call run_stage,8f,9,file-inputs-conformance,testconf-file-inputs) \
	$(call run_stage,8g,9,auth-server-conformance,testconf-auth-server) \
	$(call run_stage_info,8h,9,skills-conformance,testconf-skills) \
	$(call run_stage,9,9,keycloak,testkcl-auto) \
	echo "" | tee -a $(REPORT_DIR)/run.log; \
	echo "=== Results: $$PASS passed, $$FAIL failed, $$INFO informational ===" | tee -a $(REPORT_DIR)/run.log; \
	echo "Finished: $$(date)" | tee -a $(REPORT_DIR)/run.log; \
	echo "Per-stage logs: $(REPORT_DIR)/stage-*.log"; \
	$(MAKE) -s test-report STAGES="$$STAGES"; \
	echo "HTML report: $(REPORT_DIR)/report.html"; \
	[ $$FAIL -eq 0 ]

testkcl-auto: ## Start Keycloak if needed, run interop tests, stop after
	@if ! curl -sf http://localhost:$(KC_PORT)/realms/$(KC_REALM) > /dev/null 2>&1; then \
		echo "Starting Keycloak for interop tests..."; \
		$(MAKE) upkcl; \
		echo "Waiting for Keycloak realm..."; \
		for i in $$(seq 1 60); do \
			curl -sf http://localhost:$(KC_PORT)/realms/$(KC_REALM) > /dev/null 2>&1 && break; \
			sleep 2; \
		done; \
		KC_STARTED=1; \
	fi; \
	cd tests/keycloak && go test ./... -count=1 -timeout 120s -v; \
	EXIT=$$?; \
	if [ "$${KC_STARTED:-}" = "1" ]; then $(MAKE) downkcl; fi; \
	exit $$EXIT

test-report: ## Generate HTML report with per-stage collapsible logs
	@mkdir -p $(REPORT_DIR)
	@TIMESTAMP=$$(date '+%Y-%m-%d %H:%M:%S'); \
	COMMIT=$$(git rev-parse --short HEAD 2>/dev/null || echo "unknown"); \
	BRANCH=$$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "unknown"); \
	R=$(REPORT_DIR)/report.html; \
	echo '<!DOCTYPE html>' > $$R; \
	echo '<html><head><meta charset="utf-8"><title>MCPKit Test Report</title>' >> $$R; \
	echo '<style>' >> $$R; \
	echo 'body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; max-width: 900px; margin: 40px auto; padding: 0 20px; color: #333; }' >> $$R; \
	echo 'h1 { border-bottom: 2px solid #333; padding-bottom: 10px; }' >> $$R; \
	echo '.meta { color: #666; font-size: 14px; margin-bottom: 20px; }' >> $$R; \
	echo 'table { border-collapse: collapse; width: 100%; margin: 20px 0; }' >> $$R; \
	echo 'th, td { border: 1px solid #ddd; padding: 10px 14px; text-align: left; }' >> $$R; \
	echo 'th { background: #f5f5f5; font-weight: 600; }' >> $$R; \
	echo '.pass { color: #22863a; font-weight: 600; }' >> $$R; \
	echo '.fail { color: #cb2431; font-weight: 600; }' >> $$R; \
	echo '.skip { color: #6a737d; font-weight: 600; }' >> $$R; \
	echo '.info { color: #b08800; font-weight: 600; }' >> $$R; \
	echo '.summary-pass { background: #dcffe4; padding: 12px 20px; border-radius: 6px; font-size: 18px; }' >> $$R; \
	echo '.summary-fail { background: #ffdce0; padding: 12px 20px; border-radius: 6px; font-size: 18px; }' >> $$R; \
	echo 'details { margin: 8px 0; }' >> $$R; \
	echo 'summary { cursor: pointer; font-weight: 600; padding: 6px 0; }' >> $$R; \
	echo 'summary:hover { color: #0366d6; }' >> $$R; \
	echo 'pre { background: #f6f8fa; padding: 16px; border-radius: 6px; overflow-x: auto; font-size: 13px; max-height: 500px; overflow-y: auto; }' >> $$R; \
	echo 'code.cmd { background: #f0f0f0; padding: 2px 6px; border-radius: 3px; font-size: 13px; }' >> $$R; \
	echo '</style></head><body>' >> $$R; \
	echo "<h1>MCPKit Test Report</h1>" >> $$R; \
	echo "<div class='meta'>Branch: <strong>$$BRANCH</strong> | Commit: <code>$$COMMIT</code> | Date: $$TIMESTAMP</div>" >> $$R; \
	\
	PASS=0; FAIL=0; INFO=0; \
	echo "<table><tr><th>Stage</th><th>Result</th><th>Re-run</th></tr>" >> $$R; \
	for entry in $(STAGES); do \
		STAGE=$$(echo $$entry | cut -d: -f1); \
		RESULT=$$(echo $$entry | cut -d: -f2); \
		TARGET=$$(echo $$entry | cut -d: -f3); \
		if [ "$$RESULT" = "PASS" ]; then \
			echo "<tr><td><a href='#log-$$STAGE'>$$STAGE</a></td><td class='pass'>PASS</td><td><code class='cmd'>make $$TARGET</code></td></tr>" >> $$R; \
			PASS=$$((PASS+1)); \
		elif [ "$$RESULT" = "SKIP" ]; then \
			echo "<tr><td>$$STAGE</td><td class='skip'>SKIP</td><td><code class='cmd'>make $$TARGET</code></td></tr>" >> $$R; \
		elif [ "$$RESULT" = "INFO" ]; then \
			echo "<tr><td><a href='#log-$$STAGE'>$$STAGE</a></td><td class='info'>INFO</td><td><code class='cmd'>make $$TARGET</code></td></tr>" >> $$R; \
			INFO=$$((INFO+1)); \
		else \
			echo "<tr><td><a href='#log-$$STAGE'>$$STAGE</a></td><td class='fail'>FAIL</td><td><code class='cmd'>make $$TARGET</code></td></tr>" >> $$R; \
			FAIL=$$((FAIL+1)); \
		fi; \
	done; \
	echo "</table>" >> $$R; \
	\
	if [ $$FAIL -eq 0 ] && [ $$INFO -eq 0 ]; then \
		echo "<div class='summary-pass'>All $$PASS stages passed</div>" >> $$R; \
	elif [ $$FAIL -eq 0 ]; then \
		echo "<div class='summary-pass'>$$PASS passed, $$INFO informational (no failures)</div>" >> $$R; \
	else \
		echo "<div class='summary-fail'>$$PASS passed, $$FAIL failed, $$INFO informational</div>" >> $$R; \
	fi; \
	\
	echo "<h2>Stage Logs</h2>" >> $$R; \
	for entry in $(STAGES); do \
		STAGE=$$(echo $$entry | cut -d: -f1); \
		RESULT=$$(echo $$entry | cut -d: -f2); \
		LOGFILE=$(REPORT_DIR)/stage-$$STAGE.log; \
		OPEN=""; if [ "$$RESULT" = "FAIL" ] || [ "$$RESULT" = "INFO" ]; then OPEN=" open"; fi; \
		case "$$RESULT" in \
			PASS) CLS=pass ;; \
			INFO) CLS=info ;; \
			SKIP) CLS=skip ;; \
			*) CLS=fail ;; \
		esac; \
		echo "<details id='log-$$STAGE'$$OPEN><summary class='$$CLS'>$$STAGE — $$RESULT</summary><pre>" >> $$R; \
		if [ -f "$$LOGFILE" ]; then \
			sed 's/&/\&amp;/g; s/</\&lt;/g; s/>/\&gt;/g' "$$LOGFILE" >> $$R; \
		else \
			echo "(no log file found)" >> $$R; \
		fi; \
		echo "</pre></details>" >> $$R; \
	done; \
	echo "</body></html>" >> $$R

# =============================================================================
# Keycloak (for interop tests)
# =============================================================================

KC_IMAGE := quay.io/keycloak/keycloak:26.0
KC_PORT := 8180
KC_CONTAINER := mcpkit-keycloak
KC_REALM := mcpkit-test

upkcl: ## Start Keycloak container for interop tests (skips if already healthy)
	@if curl -sf http://localhost:$(KC_PORT)/realms/$(KC_REALM) > /dev/null 2>&1; then \
		echo "Keycloak already running on port $(KC_PORT) — skipping start"; \
	else \
		docker rm -f $(KC_CONTAINER) 2>/dev/null || true; \
		docker run -d --name $(KC_CONTAINER) \
			-p $(KC_PORT):8080 \
			-e KC_BOOTSTRAP_ADMIN_USERNAME=admin \
			-e KC_BOOTSTRAP_ADMIN_PASSWORD=admin \
			-v $(PWD)/tests/keycloak/realm.json:/opt/keycloak/data/import/realm.json \
			$(KC_IMAGE) start-dev --import-realm \
			--log-level=INFO,org.keycloak.events:DEBUG; \
		echo "Keycloak starting on port $(KC_PORT)... (realm import takes ~30s)"; \
		echo "Waiting for realm import to land before flipping master sslRequired..."; \
		for i in $$(seq 1 60); do \
			curl -sf http://localhost:$(KC_PORT)/realms/$(KC_REALM) > /dev/null 2>&1 && break; \
			sleep 1; \
		done; \
		echo "Flipping master realm sslRequired=NONE so the test admin-token grant works over HTTP..."; \
		docker exec $(KC_CONTAINER) /opt/keycloak/bin/kcadm.sh config credentials \
			--server http://localhost:8080 --realm master --user admin --password admin >/dev/null 2>&1 && \
		docker exec $(KC_CONTAINER) /opt/keycloak/bin/kcadm.sh update realms/master -s sslRequired=NONE >/dev/null && \
		echo "[upkcl] master sslRequired=NONE (the bcl_test admin-cli password grant requires it)"; \
		echo "Run 'make kcllogs' to watch startup, 'make testkcl' when ready"; \
	fi

downkcl: ## Stop Keycloak container
	docker rm -f $(KC_CONTAINER) 2>/dev/null || true

kcllogs: ## View Keycloak container logs
	docker logs -f $(KC_CONTAINER)

vet: ## Run go vet
	go vet ./...

lint: ## Run staticcheck (install: go install honnef.co/go/tools/cmd/staticcheck@latest)
	staticcheck ./...

# =============================================================================
# Security audit
# =============================================================================

vulncheck: ## Check dependencies for known vulnerabilities
	govulncheck ./...

seccheck: ## Run gosec security scanner (install: go install github.com/securego/gosec/v2/cmd/gosec@latest)
	gosec -quiet -severity=medium ./...

secrets: ## Scan for accidentally committed secrets (install: go install github.com/gitleaks/gitleaks/v8@latest)
	gitleaks detect --source . -v

verify-submodule-deps: ## Verify sub-module go.mod files reference a real root version (not v0.0.0)
	@bash scripts/verify-submodule-deps.sh

audit: vulncheck verify-submodule-deps ## Full security audit: dependency vulns + code patterns + secrets
	@echo ""
	@echo "=== gosec (informational) ==="
	@gosec -quiet -severity=high ./... || true
	@echo ""
	@echo "=== gitleaks ==="
	@gitleaks detect --source . -v 2>/dev/null || echo "gitleaks not installed (go install github.com/gitleaks/gitleaks/v8@latest)"
	@echo ""
	@echo "=== Race detection ==="
	go test -race ./... -count=1 -timeout 60s
	@echo ""
	@echo "=== Audit complete ==="

# =============================================================================
# CI (what GitHub Actions runs)
# =============================================================================

ci: test vet verify-submodule-deps ## Run tests + vet + sub-module go.mod verification (CI pipeline)

ci-full: test-race vet lint audit ## Full CI: race detection, vet, lint, security audit

# =============================================================================
# Development
# =============================================================================

serve: ## Start test server (SSE transport, default)
	go run ./cmd/testserver

serve-streamable: ## Start test server (Streamable HTTP transport)
	STREAMABLE=1 go run ./cmd/testserver

serve-both: ## Start test server (both transports)
	BOTH=1 go run ./cmd/testserver

tidy: ## Run go mod tidy on root module only
	go mod tidy

# All sub-modules with their own go.mod (root excluded — handled separately).
# Dynamically discovered so new examples / sub-packages are picked up
# automatically by tidy-all. bump-root iterates the same list but skips
# modules that don't `require` the root (see its guard below).
SUB_MODS_ALL := $(shell find . -name go.mod -not -path '*/node_modules/*' -not -path './go.mod' | sed 's|^\./||;s|/go.mod$$||' | sort)

tidy-all: ## Run go mod tidy across root + every sub-module
	@echo "==> tidy root"
	@go mod tidy
	@for mod in $(SUB_MODS_ALL); do \
		if [ -f "$$mod/go.mod" ]; then \
			echo "==> tidy $$mod"; \
			(cd $$mod && go mod tidy) || exit 1; \
		fi; \
	done

bump-root: ## Update sub-modules to require a specific root version (usage: make bump-root V=v0.1.22)
	@if [ -z "$(V)" ]; then echo "Usage: make bump-root V=v0.1.22"; exit 1; fi
	@# Only touches the root self-reference (github.com/panyam/mcpkit). Sub-module
	@# cross-references (github.com/panyam/mcpkit/ext/auth, /ext/ui) have their
	@# own independent tag timelines and must be bumped manually to a real ext/*
	@# tag — or left alone when a `replace` directive is in play.
	@for mod in $(SUB_MODS_ALL); do \
		if [ ! -f "$$mod/go.mod" ]; then continue; fi; \
		if ! grep -q "github.com/panyam/mcpkit v" "$$mod/go.mod"; then continue; fi; \
		echo "==> $$mod/go.mod: require github.com/panyam/mcpkit $(V)"; \
		(cd $$mod && go mod edit -require=github.com/panyam/mcpkit@$(V)) || exit 1; \
	done
	@$(MAKE) -s tidy-all
	@$(MAKE) -s verify-submodule-deps

# =============================================================================
# Docs site (issue 508 — GitHub Pages)
# =============================================================================

collect-walkthroughs: ## Manually mirror every examples/.../bundle/ (with a sibling walkthrough.trace.json) into docs/site/dist/docs/walkthroughs/<example-path>/. Normally runs automatically as a tail step of docs/site/Makefile's `build`.
	uv run scripts/collect_walkthroughs.py

ghbuild: ## Build docs/site/ into docs/site/dist/docs (mirrors what CI ships to gh-pages). Includes walkthrough bundles via docs/site/Makefile's build tail.
	$(MAKE) -C docs/site build

ghserve: ## Run the docs site dev server on :8085 with live rebuild
	$(MAKE) -C docs/site run

ghdeploy: ## Build + force-push docs/site/dist/docs to the gh-pages branch (one-shot manual deploy)
	$(MAKE) -C docs/site gh-pages

# =============================================================================
# Release
# =============================================================================

tag: ## Tag root + all sub-modules (usage: make tag V=v0.0.11)
	@if [ -z "$(V)" ]; then echo "Usage: make tag V=v0.0.11"; exit 1; fi
	@echo "Tagging $(V) across all modules..."
	git tag -a $(V) -m "$(V)"
	@for mod in $(SUB_MODS_TO_TAG); do \
		echo "  $$mod/$(V)"; \
		git tag -a $$mod/$(V) -m "$$mod/$(V)"; \
	done
	@echo ""
	@echo "Tags created locally. Push with:"
	@echo "  git push origin $(V) $$(echo '$(SUB_MODS_TO_TAG)' | tr ' ' '\n' | sed 's|$$|/$(V)|' | tr '\n' ' ')"

tag-push: ## Tag and push in one step (usage: make tag-push V=v0.0.11)
	@$(MAKE) tag V=$(V)
	git push origin $(V) $$(echo '$(SUB_MODS_TO_TAG)' | tr ' ' '\n' | sed 's|$$|/$(V)|' | tr '\n' ' ')

# =============================================================================
# Setup
# =============================================================================

setup-tools: ## Install development tools
	go install golang.org/x/vuln/cmd/govulncheck@latest
	go install github.com/securego/gosec/v2/cmd/gosec@latest
	go install honnef.co/go/tools/cmd/staticcheck@latest
	go install github.com/gitleaks/gitleaks/v8@latest

setup-hooks: ## Install git hooks (runs scripts/pre-push-hook.sh — skips tests when only test-report artifacts changed)
	@cp scripts/pre-push-hook.sh .git/hooks/pre-push
	@chmod +x .git/hooks/pre-push
	@echo "Installed .git/hooks/pre-push -> scripts/pre-push-hook.sh"

setup: setup-tools setup-hooks ## Full development setup

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-18s\033[0m %s\n", $$1, $$2}'

.PHONY: build test test-race test-v cover cover-html cover-func cover-all test-auth test-ui test-skills test-mcpskills build-mcpskills test-mcpskills-walkthrough test-protogen test-e2e test-experimental test-apps-playwright test-apps-playwright-docker test-apps-playwright-all test-apps-playwright-docker-all refresh-visual-gallery release-audit-apps demo-app demo-upstream testkcl testkcl-auto testall test-report smoke smoke-wire verify-dual testconfall testconf testconfauth testconf-tasks testconf-tasks-v2 testconf-mrtr testconf-file-inputs testconf-auth-server testconf-elicitation testconf-skills testconf-external-checker refresh-conformance check-conformance-stale check-local-suites-stale check-snippets check-auth-markers refresh-apps-compat-report check-apps-compat-stale vet lint vulncheck seccheck secrets verify-submodule-deps audit ci ci-full serve serve-streamable serve-both tidy tidy-all bump-root collect-walkthroughs ghbuild ghserve ghdeploy tag tag-push setup-tools setup-hooks setup upkcl downkcl kcllogs build-bridge help
.DEFAULT_GOAL := help
