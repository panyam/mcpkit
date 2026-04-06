# MCPKit Makefile

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

smoke: ## Run smoke tests (starts test servers, tests both transports via curl)
	bash scripts/smoke-test.sh

testconf: ## Run MCP conformance test suite (requires Node.js/npx)
	bash scripts/conformance-test.sh

testconfauth: ## Run MCP Auth conformance suite (client-side, requires mcpkit/auth)
	bash scripts/conformance-auth-test.sh

test-auth: ## Run auth sub-module tests
	cd auth && go test ./... -count=1 -timeout 30s

test-auth-e2e: ## Run E2E auth tests (in-process oneauth AS, no Docker)
	cd tests/e2e && go test ./... -count=1 -timeout 60s

testkcl: ## Run Keycloak auth interop tests (requires Docker, run upkcl first)
	cd tests/keycloak && go test ./... -count=1 -timeout 120s -v

REPORT_DIR := test-reports

testall: ## Run ALL tests (starts Keycloak if needed) + generate report
	@mkdir -p $(REPORT_DIR)
	@echo "=== MCPKit Comprehensive Test Suite ===" | tee $(REPORT_DIR)/run.log
	@echo "Started: $$(date)" | tee -a $(REPORT_DIR)/run.log
	@PASS=0; FAIL=0; \
	echo "" | tee -a $(REPORT_DIR)/run.log; \
	echo "--- [1/7] Unit tests ---" | tee -a $(REPORT_DIR)/run.log; \
	if go test ./... -count=1 -timeout 30s -v >> $(REPORT_DIR)/run.log 2>&1; then \
		echo "  PASS: unit" | tee -a $(REPORT_DIR)/run.log; PASS=$$((PASS+1)); \
	else \
		echo "  FAIL: unit" | tee -a $(REPORT_DIR)/run.log; FAIL=$$((FAIL+1)); \
	fi; \
	echo "--- [2/7] Race detector ---" | tee -a $(REPORT_DIR)/run.log; \
	if go test -race ./... -count=1 -timeout 60s >> $(REPORT_DIR)/run.log 2>&1; then \
		echo "  PASS: race" | tee -a $(REPORT_DIR)/run.log; PASS=$$((PASS+1)); \
	else \
		echo "  FAIL: race" | tee -a $(REPORT_DIR)/run.log; FAIL=$$((FAIL+1)); \
	fi; \
	echo "--- [3/7] Auth module ---" | tee -a $(REPORT_DIR)/run.log; \
	if (cd auth && go test ./... -count=1 -timeout 30s -v) >> $(REPORT_DIR)/run.log 2>&1; then \
		echo "  PASS: auth" | tee -a $(REPORT_DIR)/run.log; PASS=$$((PASS+1)); \
	else \
		echo "  FAIL: auth" | tee -a $(REPORT_DIR)/run.log; FAIL=$$((FAIL+1)); \
	fi; \
	echo "--- [4/7] E2E auth ---" | tee -a $(REPORT_DIR)/run.log; \
	if (cd tests/e2e && go test ./... -count=1 -timeout 60s -v) >> $(REPORT_DIR)/run.log 2>&1; then \
		echo "  PASS: e2e" | tee -a $(REPORT_DIR)/run.log; PASS=$$((PASS+1)); \
	else \
		echo "  FAIL: e2e" | tee -a $(REPORT_DIR)/run.log; FAIL=$$((FAIL+1)); \
	fi; \
	echo "--- [5/7] Conformance ---" | tee -a $(REPORT_DIR)/run.log; \
	if bash scripts/conformance-test.sh >> $(REPORT_DIR)/run.log 2>&1; then \
		echo "  PASS: conformance" | tee -a $(REPORT_DIR)/run.log; PASS=$$((PASS+1)); \
	else \
		echo "  FAIL: conformance" | tee -a $(REPORT_DIR)/run.log; FAIL=$$((FAIL+1)); \
	fi; \
	echo "--- [6/7] Auth conformance ---" | tee -a $(REPORT_DIR)/run.log; \
	if bash scripts/conformance-auth-test.sh >> $(REPORT_DIR)/run.log 2>&1; then \
		echo "  PASS: auth-conformance" | tee -a $(REPORT_DIR)/run.log; PASS=$$((PASS+1)); \
	else \
		echo "  FAIL: auth-conformance" | tee -a $(REPORT_DIR)/run.log; FAIL=$$((FAIL+1)); \
	fi; \
	echo "--- [7/7] Keycloak interop ---" | tee -a $(REPORT_DIR)/run.log; \
	$(MAKE) -s testkcl-auto >> $(REPORT_DIR)/run.log 2>&1; \
	if [ $$? -eq 0 ]; then \
		echo "  PASS: keycloak" | tee -a $(REPORT_DIR)/run.log; PASS=$$((PASS+1)); \
	else \
		echo "  FAIL: keycloak" | tee -a $(REPORT_DIR)/run.log; FAIL=$$((FAIL+1)); \
	fi; \
	echo "" | tee -a $(REPORT_DIR)/run.log; \
	echo "=== Results: $$PASS passed, $$FAIL failed ===" | tee -a $(REPORT_DIR)/run.log; \
	echo "Finished: $$(date)" | tee -a $(REPORT_DIR)/run.log; \
	echo "Full log: $(REPORT_DIR)/run.log"; \
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

test-report: ## View last test run report
	@if [ -f $(REPORT_DIR)/run.log ]; then \
		cat $(REPORT_DIR)/run.log; \
	else \
		echo "No report found. Run 'make testall' first."; \
	fi

# =============================================================================
# Keycloak (for interop tests)
# =============================================================================

KC_IMAGE := quay.io/keycloak/keycloak:26.0
KC_PORT := 8180
KC_CONTAINER := mcpkit-keycloak
KC_REALM := mcpkit-test

upkcl: ## Start Keycloak container for interop tests
	@docker rm -f $(KC_CONTAINER) 2>/dev/null || true
	docker run -d --name $(KC_CONTAINER) \
		-p $(KC_PORT):8080 \
		-e KC_BOOTSTRAP_ADMIN_USERNAME=admin \
		-e KC_BOOTSTRAP_ADMIN_PASSWORD=admin \
		-v $(PWD)/tests/keycloak/realm.json:/opt/keycloak/data/import/realm.json \
		$(KC_IMAGE) start-dev --import-realm
	@echo "Keycloak starting on port $(KC_PORT)... (realm import takes ~30s)"
	@echo "Run 'make kcllogs' to watch startup, 'make testkcl' when ready"

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

audit: vulncheck ## Full security audit: dependency vulns + code patterns + secrets
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

ci: test vet ## Run tests + vet (CI pipeline)

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

tidy: ## Run go mod tidy
	go mod tidy

# =============================================================================
# Setup
# =============================================================================

setup-tools: ## Install development tools
	go install golang.org/x/vuln/cmd/govulncheck@latest
	go install github.com/securego/gosec/v2/cmd/gosec@latest
	go install honnef.co/go/tools/cmd/staticcheck@latest
	go install github.com/gitleaks/gitleaks/v8@latest

setup-hooks: ## Install git hooks
	cp -f .git/hooks/pre-push.sample .git/hooks/pre-push 2>/dev/null || true
	echo '#!/bin/sh\nset -e\ncd "$$(git rev-parse --show-toplevel)"\ngo test ./...' > .git/hooks/pre-push
	chmod +x .git/hooks/pre-push

setup: setup-tools setup-hooks ## Full development setup

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-18s\033[0m %s\n", $$1, $$2}'

.PHONY: build test test-race test-v test-auth test-auth-e2e testkcl testkcl-auto testall test-report smoke testconf testconfauth vet lint vulncheck seccheck secrets audit ci ci-full serve serve-streamable serve-both tidy setup-tools setup-hooks setup upkcl downkcl kcllogs help
.DEFAULT_GOAL := help
