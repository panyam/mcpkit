# MCPKit Makefile

# Sub-modules that get tagged alongside the root module
SUB_MODS_TO_TAG := ext/auth ext/ui experimental/ext/protogen cmd/testclient tests/e2e tests/keycloak

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
	go test -coverprofile=$(REPORT_DIR)/coverage.out ./... -count=1 -timeout 30s
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

testconfall: testconf testconfauth

testconf: ## Run MCP conformance test suite (requires Node.js/npx)
	bash scripts/conformance-test.sh

testconf-tasks: ## Run MCP Tasks v1 conformance (builds + starts server, runs tests, tears down)
	@(cd examples/tasks && go build -o tasks .) && \
	examples/tasks/tasks -addr :18091 & PID=$$!; \
	sleep 1; \
	(cd conformance && npm install --silent && \
	SERVER_URL=http://localhost:18091/mcp npx tsx --test tasks/scenarios.test.ts); \
	RC=$$?; kill $$PID 2>/dev/null; wait $$PID 2>/dev/null; exit $$RC

testconf-tasks-v2: ## Run MCP Tasks v2 conformance (builds + starts server, runs tests, tears down)
	@(cd examples/tasks-v2 && go build -o tasks-v2 .) && \
	examples/tasks-v2/tasks-v2 --serve -addr :18092 & PID=$$!; \
	sleep 1; \
	(cd conformance && npm install --silent && \
	SERVER_URL=http://localhost:18092/mcp npx tsx --test tasks-v2/scenarios.test.ts); \
	RC=$$?; kill $$PID 2>/dev/null; wait $$PID 2>/dev/null; exit $$RC

testconf-elicitation: ## Run elicitation conformance suite (SEP-1036 URL mode + form, requires Node.js, target server must be running)
	cd conformance && npm install --silent && SERVER_URL=$${SERVER_URL:-http://localhost:8080/mcp} npx tsx --test elicitation/scenarios.test.ts

testconfauth: ## Run MCP Auth conformance suite (client-side, requires mcpkit/auth)
	bash scripts/conformance-auth-test.sh

test-auth: ## Run auth sub-module tests
	cd ext/auth && go test ./... -count=1 -timeout 30s

test-ui: ## Run UI extension sub-module tests
	cd ext/ui && $(MAKE) test

build-bridge: ## Compile mcp-app-bridge.ts → .js (delegates to ext/ui)
	cd ext/ui && $(MAKE) build-bridge

test-protogen: ## Run protogen sub-module tests + e2e example
	cd experimental/ext/protogen && go test ./... -count=1 -timeout 30s && $(MAKE) test-e2e

test-e2e: ## Run all E2E tests (auth, apps — no Docker)
	cd tests/e2e && go test ./... -count=1 -timeout 60s

test-experimental: ## Run experimental POC tests (ext/events library + telegram-events + discord-events)
	cd experimental/ext/events && go test ./... -count=1 -timeout 60s
	cd experimental/telegram-events && go test ./... -count=1 -timeout 60s
	cd experimental/discord-events && go test ./... -count=1 -timeout 60s

test-apps-playwright: ## Run ext-apps Playwright tests against testserver (needs Node.js + Playwright)
	bash scripts/apps-playwright-test.sh

testkcl: ## Run Keycloak auth interop tests (requires Docker, run upkcl first)
	cd tests/keycloak && go test ./... -count=1 -timeout 120s -v

REPORT_DIR := tests/reports

# run_stage runs a make target as a testall stage with per-stage log files.
# Each stage writes to $(REPORT_DIR)/stage-<label>.log (not the shared run.log).
# Usage: $(call run_stage,STEP_NUM,TOTAL,LABEL,MAKE_TARGET)
# Shell vars PASS, FAIL, STAGES must be initialized by the caller.
define run_stage
	STAGE_LOG=$(REPORT_DIR)/stage-$(3).log; \
	echo "--- [$(1)/$(2)] $(3) ---" | tee -a $(REPORT_DIR)/run.log; \
	echo "=== Stage $(1)/$(2): $(3) (make $(4)) ===" > $$STAGE_LOG; \
	echo "Started: $$(date)" >> $$STAGE_LOG; \
	if $(MAKE) $(4) >> $$STAGE_LOG 2>&1; then \
		echo "  PASS: $(3)" | tee -a $(REPORT_DIR)/run.log; PASS=$$((PASS+1)); STAGES="$$STAGES $(3):PASS:$(4)"; \
	else \
		echo "  FAIL: $(3)" | tee -a $(REPORT_DIR)/run.log; FAIL=$$((FAIL+1)); STAGES="$$STAGES $(3):FAIL:$(4)"; \
		echo "  --- $(3) tail ---"; tail -20 $$STAGE_LOG; echo "  ---"; \
	fi; \
	echo "Finished: $$(date)" >> $$STAGE_LOG;
endef

testall: ## Run ALL tests (starts Keycloak if needed) + per-stage HTML reports
	@mkdir -p $(REPORT_DIR)
	@rm -f $(REPORT_DIR)/stage-*.log
	@echo "=== MCPKit Comprehensive Test Suite ===" | tee $(REPORT_DIR)/run.log
	@echo "Started: $$(date)" | tee -a $(REPORT_DIR)/run.log
	@PASS=0; FAIL=0; STAGES=""; \
	echo "" | tee -a $(REPORT_DIR)/run.log; \
	$(call run_stage,1,12,unit+coverage,cover-html) \
	$(call run_stage,2,12,race,test-race) \
	$(call run_stage,3,12,auth,test-auth) \
	$(call run_stage,4,12,ui,test-ui) \
	$(call run_stage,5,12,protogen,test-protogen) \
	$(call run_stage,6,12,e2e,test-e2e) \
	$(call run_stage,7,12,experimental,test-experimental) \
	$(call run_stage,8,12,conformance,testconf) \
	$(call run_stage,9,12,auth-conformance,testconfauth) \
	$(call run_stage,10,12,tasks-conformance,testconf-tasks) \
	$(call run_stage,11,12,tasks-v2-conformance,testconf-tasks-v2) \
	$(call run_stage,12,12,keycloak,testkcl-auto) \
	echo "" | tee -a $(REPORT_DIR)/run.log; \
	echo "=== Results: $$PASS passed, $$FAIL failed ===" | tee -a $(REPORT_DIR)/run.log; \
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
	PASS=0; FAIL=0; \
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
		else \
			echo "<tr><td><a href='#log-$$STAGE'>$$STAGE</a></td><td class='fail'>FAIL</td><td><code class='cmd'>make $$TARGET</code></td></tr>" >> $$R; \
			FAIL=$$((FAIL+1)); \
		fi; \
	done; \
	echo "</table>" >> $$R; \
	\
	if [ $$FAIL -eq 0 ]; then \
		echo "<div class='summary-pass'>All $$PASS stages passed</div>" >> $$R; \
	else \
		echo "<div class='summary-fail'>$$PASS passed, $$FAIL failed</div>" >> $$R; \
	fi; \
	\
	echo "<h2>Stage Logs</h2>" >> $$R; \
	for entry in $(STAGES); do \
		STAGE=$$(echo $$entry | cut -d: -f1); \
		RESULT=$$(echo $$entry | cut -d: -f2); \
		LOGFILE=$(REPORT_DIR)/stage-$$STAGE.log; \
		OPEN=""; if [ "$$RESULT" = "FAIL" ]; then OPEN=" open"; fi; \
		echo "<details id='log-$$STAGE'$$OPEN><summary class='$$( [ "$$RESULT" = "PASS" ] && echo pass || echo fail )'>$$STAGE — $$RESULT</summary><pre>" >> $$R; \
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

# All sub-modules (including tests/*) that have their own go.mod and require
# the root module. Used by tidy-all and bump-root targets.
SUB_MODS_ALL := ext/auth ext/ui experimental/ext/protogen cmd/testclient tests/e2e tests/keycloak

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

.PHONY: build test test-race test-v cover cover-html cover-func cover-all test-auth test-ui test-protogen test-e2e test-experimental test-apps-playwright testkcl testkcl-auto testall test-report smoke testconfall testconf testconfauth testconf-tasks testconf-tasks-v2 vet lint vulncheck seccheck secrets verify-submodule-deps audit ci ci-full serve serve-streamable serve-both tidy tidy-all bump-root tag tag-push setup-tools setup-hooks setup upkcl downkcl kcllogs build-bridge help
.DEFAULT_GOAL := help
