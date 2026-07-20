# MCPKit Makefile

# Sub-modules that get tagged alongside the root module. Every importable
# sub-module (its own go.mod, `require`s the root) needs a tag here so
# downstream can `go get <module>@vX.Y.Z` — `replace` directives are ignored
# by non-main modules. ext/tasks, ext/skills, stores/redis, and the
# experimental events modules were added once they shipped their own go.mod.
SUB_MODS_TO_TAG := \
	agent agent/host agent/store/redis agent/store/gorm \
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

pg: ## Playground: boot the demo MCP server + launch agentchat's TUI (needs a local OpenAI-compatible model; see examples/playground/README.md)
	bash scripts/playground.sh

test-agent: ## Run agent sub-module tests
	cd agent && go test ./... -count=1 -timeout 30s
	cd agent/store/redis && go test ./... -count=1 -timeout 60s
	cd agent/store/gorm && go test ./... -count=1 -timeout 60s
	cd agent/host && go test ./... -count=1 -timeout 60s
	cd cmd/agentchat && go test ./... -count=1 -timeout 60s
	cd examples/agents/agent-async && go test ./... -count=1 -timeout 60s
	cd examples/agents/multi-agent && go test ./... -count=1 -timeout 60s
	cd examples/skills && go test ./... -count=1 -timeout 60s -run TestAgentScenario
	cd examples/tasks-v2 && go test ./... -count=1 -timeout 60s -run TestAgentScenario

test-auth: ## Run auth sub-module tests
	@bash scripts/test-auth.sh

test-otel: ## Run SEP-414 ext/otel adapter sub-module tests
	@bash scripts/test-otel.sh

test-otel-example: ## Run the examples/otel/stdout smoke test
	@bash scripts/test-otel-example.sh

test-ui: ## Run UI extension sub-module tests
	@bash ext/ui/scripts/test.sh

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
	@bash ext/ui/scripts/build-bridge.sh

test-protogen: ## Run protogen sub-module tests + e2e example
	@bash scripts/test-protogen.sh

test-e2e: ## Run all E2E tests (auth, apps — no Docker)
	@bash scripts/test-e2e.sh

test-experimental: ## Run all experimental POC tests
	@bash experimental/scripts/test-events.sh
	@bash experimental/scripts/test-events-clients-go.sh
	@bash experimental/scripts/test-events-stores-gorm.sh
	@bash experimental/scripts/test-events-discord.sh
	@bash experimental/scripts/test-events-telegram.sh

test-experimental-events: ## Run experimental ext/events library tests
	@bash experimental/scripts/test-events.sh

test-experimental-events-clients-go: ## Run experimental ext/events Go client SDK tests
	@bash experimental/scripts/test-events-clients-go.sh

test-experimental-events-stores-gorm: ## Run experimental ext/events GORM stores (sqlite + inmemory; no Docker required)
	@bash experimental/scripts/test-events-stores-gorm.sh

test-experimental-events-stores-gorm-pg: ## Run experimental ext/events GORM stores against a real Postgres container (Docker)
	$(MAKE) -C experimental test-events-stores-gorm-pg

test-experimental-events-stores-redis: ## Run experimental ext/events Redis pubsub Emitter (miniredis; no Docker required)
	@bash experimental/scripts/test-events-stores-redis.sh

test-experimental-events-stores-redis-real: ## Run experimental ext/events Redis pubsub Emitter against a real Redis container (Docker)
	$(MAKE) -C experimental test-events-stores-redis-real

test-experimental-events-discord: ## Run experimental events Discord example tests
	@bash experimental/scripts/test-events-discord.sh

test-experimental-events-telegram: ## Run experimental events Telegram example tests
	@bash experimental/scripts/test-events-telegram.sh

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
	@bash scripts/release-audit-apps.sh

demo-app: ## Browse a compat fixture interactively. Default: mcpkit-Go server + basic-host (no LLM needed). basic-host runs on :8080; open it manually (or pass OPEN=1 to auto-open). Override with RENDERER=mcpjam for wire inspection. Usage: make demo-app EXAMPLE=<name>.
	EXAMPLE=$(EXAMPLE) SERVER=$${SERVER:-go} RENDERER=$${RENDERER:-basic-host} OPEN=$(OPEN) uv run scripts/apps_demo.py

demo-upstream: ## Browse the upstream TS reference server instead of the Go fixture. Same axes as demo-app. Use this for SKIP examples (lazy-auth-server, video-resource-server, qr-server, say-server) that have no Go drop-in. Usage: make demo-upstream EXAMPLE=<name>.
	EXAMPLE=$(EXAMPLE) SERVER=upstream RENDERER=$${RENDERER:-basic-host} OPEN=$(OPEN) uv run scripts/apps_demo.py

testkcl: ## Run Keycloak auth interop tests (requires Docker, run upkcl first)
	cd tests/keycloak && go test ./... -count=1 -timeout 120s -v

REPORT_DIR := tests/reports

testall: ## Run ALL tests (starts Keycloak if needed) + per-stage HTML reports
	@REPORT_DIR=$(REPORT_DIR) bash scripts/testall.sh

testkcl-auto: ## Start Keycloak if needed, run interop tests, stop after
	@bash scripts/testkcl-auto.sh

test-report: ## Generate HTML report with per-stage collapsible logs
	@REPORT_DIR=$(REPORT_DIR) bash scripts/test-report.sh "$(STAGES)"

# =============================================================================
# Keycloak (for interop tests)
# =============================================================================

KC_IMAGE := quay.io/keycloak/keycloak:26.0
KC_PORT := 8180
KC_CONTAINER := mcpkit-keycloak
KC_REALM := mcpkit-test

upkcl: ## Start Keycloak container for interop tests (skips if already healthy)
	@bash scripts/keycloak-up.sh

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

ghbuild: ## Build docs/site/ into docs/site/dist/docs (mirrors what CI ships to gh-pages). Includes walkthrough bundles via the build tail.
	@bash docs/site/scripts/build.sh

ghserve: ## Run the docs site dev server on :8085 with live rebuild
	$(MAKE) -C docs/site run

ghdeploy: ## Build + force-push docs/site/dist/docs to the gh-pages branch (one-shot manual deploy)
	@bash docs/site/scripts/gh-pages.sh

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
