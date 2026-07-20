# MCPKit justfile
#
# Root task runner. Sub-directory justfiles (conformance/, experimental/,
# docs/site/, ext/ui/, examples/*) are delegated to via
# `just -f <dir>/justfile <recipe>`.

# Sub-modules that get tagged alongside the root module. Every importable
# sub-module (its own go.mod, `require`s the root) needs a tag here so
# downstream can `go get <module>@vX.Y.Z` — `replace` directives are ignored
# by non-main modules. ext/tasks, ext/skills, stores/redis, and the
# experimental events modules were added once they shipped their own go.mod.
SUB_MODS_TO_TAG := "agent agent/host agent/store/redis agent/store/gorm ext/auth ext/otel ext/ui ext/tasks ext/skills stores/redis experimental/ext/events experimental/ext/events/stores/memory experimental/ext/events/stores/gorm experimental/ext/events/stores/redis experimental/ext/events/clients/go cmd/testclient cmd/common cmd/mcpskills cmd/agentchat examples/mcpskills-walkthrough tests/e2e tests/keycloak"

REPORT_DIR := "tests/reports"

# Discovers every sub-module go.mod (root excluded). Kept as a command string
# (not a backtick expression) so `find` only runs when a consuming recipe
# executes, not on every just invocation. Consumers: tidy-all, bump-root.
SUB_MODS_FIND := "find . -name go.mod -not -path '*/node_modules/*' -not -path './go.mod' | sed 's|^\\./||;s|/go.mod$||' | sort"

# Keycloak (for interop tests)
KC_IMAGE := "quay.io/keycloak/keycloak:26.0"
KC_PORT := "8180"
KC_CONTAINER := "mcpkit-keycloak"
KC_REALM := "mcpkit-test"
# Probed by upkcl / testkcl-auto to detect a healthy realm import.
KC_REALM_URL := "http://localhost:" + KC_PORT + "/realms/" + KC_REALM

# Show available recipes
default:
    @just --list --unsorted

# Show available recipes
help:
    @just --list --unsorted

# =============================================================================
# Build & test
# =============================================================================

# Build all packages
build:
    go build ./...

# Run unit tests
test:
    go test ./... -count=1 -timeout 30s

# Run unit tests with race detector
test-race:
    go test -race ./... -count=1 -timeout 60s

# Run unit tests with verbose output
test-v:
    go test ./... -count=1 -timeout 30s -v

# Run tests with coverage summary (root module only)
cover:
    go test -cover ./... -count=1 -timeout 30s

# Run tests with coverage and generate HTML report (root module only)
cover-html:
    @mkdir -p {{REPORT_DIR}}
    go test -coverprofile={{REPORT_DIR}}/coverage.out ./... -count=1 -timeout 120s
    go tool cover -html={{REPORT_DIR}}/coverage.out -o {{REPORT_DIR}}/coverage.html
    @echo "Coverage report: {{REPORT_DIR}}/coverage.html"

# Show per-function coverage sorted by lowest (root module only)
cover-func:
    @mkdir -p {{REPORT_DIR}}
    go test -coverprofile={{REPORT_DIR}}/coverage.out ./... -count=1 -timeout 30s
    go tool cover -func={{REPORT_DIR}}/coverage.out | sort -k3 -n | head -30

# Run coverage across root + all sub-modules, generate per-module HTML reports
cover-all:
    #!/usr/bin/env bash
    set -eu
    mkdir -p {{REPORT_DIR}}
    echo "==> coverage: root module"
    go test -coverprofile={{REPORT_DIR}}/coverage-root.out ./... -count=1 -timeout 30s
    go tool cover -html={{REPORT_DIR}}/coverage-root.out -o {{REPORT_DIR}}/coverage-root.html
    for mod in ext/auth ext/ui; do
        echo "==> coverage: $mod"
        (cd $mod && go test -coverprofile=../../{{REPORT_DIR}}/coverage-$(echo $mod | tr / -).out ./... -count=1 -timeout 30s) || true
        go tool cover -html={{REPORT_DIR}}/coverage-$(echo $mod | tr / -).out -o {{REPORT_DIR}}/coverage-$(echo $mod | tr / -).html 2>/dev/null || true
    done
    echo ""
    echo "Coverage reports:"
    ls -1 {{REPORT_DIR}}/coverage-*.html 2>/dev/null

# Run smoke tests (starts test servers, tests both transports via curl)
smoke:
    bash scripts/smoke-test.sh

# Boot each --wire example and assert wire selection took effect (issue 824)
smoke-wire:
    bash scripts/smoke-wire.sh

# Run each auto-drivable example walkthrough on both wires; assert behavioral parity (issue 478)
verify-dual:
    bash scripts/verify-dual.sh

# Conformance shims — actual logic lives in conformance/justfile.

# Run base + auth conformance only (delegates to conformance/justfile)
testconfall:
    just -f conformance/justfile test

# Run MCP conformance test suite (delegates to conformance/justfile)
testconf:
    just -f conformance/justfile testconf

# Run MCP Auth conformance suite (delegates to conformance/justfile)
testconfauth:
    just -f conformance/justfile testconfauth

# Run MCP Tasks v1 conformance (delegates to conformance/justfile)
testconf-tasks:
    just -f conformance/justfile testconf-tasks

# Run SEP-2663 tasks conformance — upstream + mcpkit-local sentinel (delegates to conformance/justfile)
testconf-tasks-v2:
    just -f conformance/justfile testconf-tasks-v2

# Run SEP-2322 MRTR conformance — upstream + mcpkit-local sentinel (delegates to conformance/justfile)
testconf-mrtr:
    just -f conformance/justfile testconf-mrtr

# Run SEP-2356 file-inputs conformance — fork-based (delegates to conformance/justfile)
testconf-file-inputs:
    just -f conformance/justfile testconf-file-inputs

# Run server-side auth conformance — fork-based, RFC 9728 + RFC 8414 (delegates to conformance/justfile)
testconf-auth-server:
    just -f conformance/justfile testconf-auth-server

# Run SEP-1036 elicitation conformance (delegates to conformance/justfile)
testconf-elicitation:
    just -f conformance/justfile testconf-elicitation

# Run SEP-2575 stateless conformance — drives examples/stateless (delegates to conformance/justfile)
testconf-stateless:
    just -f conformance/justfile testconf-stateless

# Run SEP-2640 skills conformance — fork-based (delegates to conformance/justfile)
testconf-skills:
    just -f conformance/justfile testconf-skills

# Audit mcpkit against modelcontextprotocol/conformance@main → conformance/UPSTREAM_AUDIT.md (informational; delegates to conformance/justfile)
testconf-upstream-audit:
    just -f conformance/justfile testconf-upstream-audit

# Grade the mcpkit CLIENT against the external stateless-draft checker (live network, not a CI gate; delegates to conformance/justfile)
testconf-external-checker:
    just -f conformance/justfile testconf-external-checker

# Regenerate CONFORMANCE.md from upstream tier-check + traceability (delegates to conformance/justfile)
refresh-conformance:
    just -f conformance/justfile refresh-conformance

# Fail if CONFORMANCE.md is stale relative to current testserver + upstream (CI gate)
check-conformance-stale: check-local-suites-stale
    just -f conformance/justfile check-conformance-stale

# CI gate — fail if conformance/local-suites.yaml drifts from the justfile (cases A/B/C)
check-local-suites-stale:
    uv run scripts/check_local_suites.py

# CI gate — fail if docs/GETTING_STARTED.md Go snippets drift from examples/getting-started/ (issue 853)
check-snippets:
    go run ./tools/check-snippets

# Fail if an AUTH_SPEC_COVERAGE.md clause lacks its inline ext/auth marker (issue 504)
check-auth-markers:
    go run ./tools/check-auth-markers

# Regenerate conformance/apps/COMPAT.md from umbrella tracking issue (#533). Uses gh CLI.
refresh-apps-compat-report:
    ./scripts/refresh-apps-compat-report.sh

# Fail if conformance/apps/COMPAT.md is stale relative to umbrella #533 (CI gate)
check-apps-compat-stale: refresh-apps-compat-report
    #!/usr/bin/env bash
    if ! git diff --exit-code conformance/apps/COMPAT.md; then
        echo "::error::conformance/apps/COMPAT.md is stale."
        echo "::error::Run 'just refresh-apps-compat-report' locally and commit the diff."
        exit 1
    fi

# Playground: boot the demo MCP server + launch agentchat's TUI (needs a local OpenAI-compatible model; see examples/playground/README.md)
pg:
    bash scripts/playground.sh

# Run go test in one sub-module (shared by the test-* recipes below)
_go-test dir timeout="30s" extra="":
    cd {{dir}} && go test ./... -count=1 -timeout {{timeout}} {{extra}}

# Run agent sub-module tests
test-agent:
    @{{just_executable()}} _go-test agent
    @{{just_executable()}} _go-test agent/store/redis 60s
    @{{just_executable()}} _go-test agent/store/gorm 60s
    @{{just_executable()}} _go-test agent/host 60s
    @{{just_executable()}} _go-test cmd/agentchat 60s
    @{{just_executable()}} _go-test examples/agents/agent-async 60s
    @{{just_executable()}} _go-test examples/agents/multi-agent 60s
    @{{just_executable()}} _go-test examples/skills 60s "-run TestAgentScenario"
    @{{just_executable()}} _go-test examples/tasks-v2 60s "-run TestAgentScenario"

# Run auth sub-module tests
test-auth:
    @bash scripts/test-auth.sh

# Run SEP-414 ext/otel adapter sub-module tests
test-otel:
    @bash scripts/test-otel.sh

# Run the examples/otel/stdout smoke test
test-otel-example:
    @bash scripts/test-otel-example.sh

# Run UI extension sub-module tests
test-ui:
    @bash ext/ui/scripts/test.sh

# Run skills extension sub-module tests (SEP-2640, experimental)
test-skills: (_go-test "ext/skills")

# Run cmd/mcpskills CLI smoke tests (SEP-2640)
test-mcpskills: (_go-test "cmd/mcpskills" "60s")

# Build the mcpskills CLI binary into ./bin/mcpskills
build-mcpskills:
    @mkdir -p bin
    cd cmd/mcpskills && go build -o ../../bin/mcpskills .
    @echo "wrote bin/mcpskills"

# Run the mcpskills CLI walkthrough non-interactively as a CI smoke test
test-mcpskills-walkthrough:
    cd examples/mcpskills-walkthrough && go run . --non-interactive

# Compile mcp-app-bridge.ts → .js (delegates to ext/ui)
build-bridge:
    @bash ext/ui/scripts/build-bridge.sh

# Run protogen sub-module tests + e2e example
test-protogen:
    @bash scripts/test-protogen.sh

# Run all E2E tests (auth, apps — no Docker)
test-e2e:
    @bash scripts/test-e2e.sh

# Run all experimental POC tests
test-experimental:
    @bash experimental/scripts/test-events.sh
    @bash experimental/scripts/test-events-clients-go.sh
    @bash experimental/scripts/test-events-stores-gorm.sh
    @bash experimental/scripts/test-events-discord.sh
    @bash experimental/scripts/test-events-telegram.sh

# Run experimental ext/events library tests
test-experimental-events:
    @bash experimental/scripts/test-events.sh

# Run experimental ext/events Go client SDK tests
test-experimental-events-clients-go:
    @bash experimental/scripts/test-events-clients-go.sh

# Run experimental ext/events GORM stores (sqlite + inmemory; no Docker required)
test-experimental-events-stores-gorm:
    @bash experimental/scripts/test-events-stores-gorm.sh

# Run experimental ext/events GORM stores against a real Postgres container (Docker)
test-experimental-events-stores-gorm-pg:
    just -f experimental/justfile test-events-stores-gorm-pg

# Run experimental ext/events Redis pubsub Emitter (miniredis; no Docker required)
test-experimental-events-stores-redis:
    @bash experimental/scripts/test-events-stores-redis.sh

# Run experimental ext/events Redis pubsub Emitter against a real Redis container (Docker)
test-experimental-events-stores-redis-real:
    just -f experimental/justfile test-events-stores-redis-real

# Run experimental events Discord example tests
test-experimental-events-discord:
    @bash experimental/scripts/test-events-discord.sh

# Run experimental events Telegram example tests
test-experimental-events-telegram:
    @bash experimental/scripts/test-events-telegram.sh

# Run ext-apps Playwright tests against testserver (needs Node.js + Playwright). EXAMPLE=<name> picks a fixture.
test-apps-playwright:
    uv run scripts/apps_playwright_test.py

# Same as test-apps-playwright but inside upstream's playwright Docker image — CI-identical baselines
test-apps-playwright-docker:
    uv run scripts/apps_playwright_test.py --docker

# Sweep every registered compat fixture sequentially. Exits non-zero if any fail.
test-apps-playwright-all:
    uv run scripts/apps_playwright_test.py --all

# --all + --docker. The canonical visual gate across all 21 compat fixtures.
test-apps-playwright-docker-all:
    uv run scripts/apps_playwright_test.py --docker --all

# Regenerate the side-by-side baselines gallery (mcpkit vs upstream PNGs with bordered-box diff regions). Manual; run after a successful docker-all + commit the regenerated artifacts.
refresh-visual-gallery:
    uv run scripts/apps_visual_gallery.py
    @echo "Gallery refreshed. Commit docs/site/content/conformance/apps/visual-gallery/ + docs/site/static/conformance/apps/visual-gallery/."

# Release-time apps/compat audit umbrella — fully end-to-end: refresh ext-apps clone → docker-all (parity + visual gate) → regenerate gallery → commit + push the gallery → ghdeploy. Single command for "release-time, just do everything."
release-audit-apps:
    bash scripts/release-audit-apps.sh

# Browse a compat fixture interactively. Default: mcpkit-Go server + basic-host (no LLM needed). basic-host runs on :8080; open it manually (or pass OPEN=1 to auto-open). Override with RENDERER=mcpjam for wire inspection. Usage: just demo-app <name>
demo-app EXAMPLE OPEN="":
    EXAMPLE={{EXAMPLE}} SERVER=${SERVER:-go} RENDERER=${RENDERER:-basic-host} OPEN={{OPEN}} uv run scripts/apps_demo.py

# Browse the upstream TS reference server instead of the Go fixture. Same axes as demo-app. Use this for SKIP examples (lazy-auth-server, video-resource-server, qr-server, say-server) that have no Go drop-in. Usage: just demo-upstream <name>
demo-upstream EXAMPLE OPEN="":
    EXAMPLE={{EXAMPLE}} SERVER=upstream RENDERER=${RENDERER:-basic-host} OPEN={{OPEN}} uv run scripts/apps_demo.py

# Run Keycloak auth interop tests (requires Docker, run upkcl first)
testkcl:
    cd tests/keycloak && go test ./... -count=1 -timeout 120s -v

# Run ALL tests (starts Keycloak if needed) + per-stage HTML reports
testall:
    REPORT_DIR={{REPORT_DIR}} bash scripts/testall.sh

# Start Keycloak if needed, run interop tests, stop after
testkcl-auto:
    @bash scripts/testkcl-auto.sh

# Generate HTML report with per-stage collapsible logs
test-report STAGES:
    @REPORT_DIR={{REPORT_DIR}} bash scripts/test-report.sh "{{STAGES}}"

# =============================================================================
# Keycloak (for interop tests)
# =============================================================================

# Start Keycloak container for interop tests (skips if already healthy)
upkcl:
    @bash scripts/keycloak-up.sh

# Stop Keycloak container
downkcl:
    docker rm -f {{KC_CONTAINER}} 2>/dev/null || true

# View Keycloak container logs
kcllogs:
    docker logs -f {{KC_CONTAINER}}

# Run go vet
vet:
    go vet ./...

# Run staticcheck (install: go install honnef.co/go/tools/cmd/staticcheck@latest)
lint:
    staticcheck ./...

# =============================================================================
# Security audit
# =============================================================================

# Check dependencies for known vulnerabilities
vulncheck:
    govulncheck ./...

# Run gosec security scanner (install: go install github.com/securego/gosec/v2/cmd/gosec@latest)
seccheck:
    gosec -quiet -severity=medium ./...

# Scan for accidentally committed secrets (install: go install github.com/gitleaks/gitleaks/v8@latest)
secrets:
    gitleaks detect --source . -v

# Verify sub-module go.mod files reference a real root version (not v0.0.0)
verify-submodule-deps:
    @bash scripts/verify-submodule-deps.sh

# Full security audit: dependency vulns + code patterns + secrets
audit: vulncheck verify-submodule-deps
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

# Run tests + vet + sub-module go.mod verification (CI pipeline)
ci: test vet verify-submodule-deps

# Full CI: race detection, vet, lint, security audit
ci-full: test-race vet lint audit

# =============================================================================
# Development
# =============================================================================

# Start test server (SSE transport, default)
serve:
    go run ./cmd/testserver

# Start test server (Streamable HTTP transport)
serve-streamable:
    STREAMABLE=1 go run ./cmd/testserver

# Start test server (both transports)
serve-both:
    BOTH=1 go run ./cmd/testserver

# Run go mod tidy on root module only
tidy:
    go mod tidy

# Run go mod tidy across root + every sub-module
tidy-all:
    #!/usr/bin/env bash
    set -eu
    echo "==> tidy root"
    go mod tidy
    # All sub-modules with their own go.mod (root excluded — handled
    # separately). Dynamically discovered so new examples / sub-packages
    # are picked up automatically.
    for mod in $({{SUB_MODS_FIND}}); do
        if [ -f "$mod/go.mod" ]; then
            echo "==> tidy $mod"
            (cd $mod && go mod tidy) || exit 1
        fi
    done

# Update sub-modules to require a specific root version (usage: just bump-root v0.1.22)
bump-root V: && tidy-all verify-submodule-deps
    #!/usr/bin/env bash
    set -eu
    # Only touches the root self-reference (github.com/panyam/mcpkit). Sub-module
    # cross-references (github.com/panyam/mcpkit/ext/auth, /ext/ui) have their
    # own independent tag timelines and must be bumped manually to a real ext/*
    # tag — or left alone when a `replace` directive is in play.
    for mod in $({{SUB_MODS_FIND}}); do
        if [ ! -f "$mod/go.mod" ]; then continue; fi
        if ! grep -q "github.com/panyam/mcpkit v" "$mod/go.mod"; then continue; fi
        echo "==> $mod/go.mod: require github.com/panyam/mcpkit {{V}}"
        (cd $mod && go mod edit -require=github.com/panyam/mcpkit@{{V}}) || exit 1
    done

# =============================================================================
# Docs site (issue 508 — GitHub Pages)
# =============================================================================

# Manually mirror every examples/.../bundle/ (with a sibling walkthrough.trace.json) into docs/site/dist/docs/walkthroughs/<example-path>/. Normally runs automatically as a tail step of docs/site/justfile's `build`.
collect-walkthroughs:
    uv run scripts/collect_walkthroughs.py

# Build docs/site/ into docs/site/dist/docs (mirrors what CI ships to gh-pages). Includes walkthrough bundles via docs/site/justfile's build tail.
ghbuild:
    @bash docs/site/scripts/build.sh

# Run the docs site dev server on :8085 with live rebuild
ghserve:
    just -f docs/site/justfile run

# Build + force-push docs/site/dist/docs to the gh-pages branch (one-shot manual deploy)
ghdeploy:
    @bash docs/site/scripts/gh-pages.sh

# =============================================================================
# Release
# =============================================================================

# Emit the full ref list for pushing a release: root tag + every sub-module tag
_tag-list V:
    @echo "{{V}} $(echo '{{SUB_MODS_TO_TAG}}' | tr ' ' '\n' | sed 's|$|/{{V}}|' | tr '\n' ' ')"

# Tag root + all sub-modules (usage: just tag v0.0.11)
tag V:
    #!/usr/bin/env bash
    set -eu
    echo "Tagging {{V}} across all modules..."
    git tag -a {{V}} -m "{{V}}"
    for mod in {{SUB_MODS_TO_TAG}}; do
        echo "  $mod/{{V}}"
        git tag -a "$mod/{{V}}" -m "$mod/{{V}}"
    done
    echo ""
    echo "Tags created locally. Push with:"
    echo "  git push origin $({{just_executable()}} _tag-list {{V}})"

# Tag and push in one step (usage: just tag-push v0.0.11)
tag-push V:
    #!/usr/bin/env bash
    set -eu
    {{just_executable()}} tag {{V}}
    git push origin $({{just_executable()}} _tag-list {{V}})

# =============================================================================
# Setup
# =============================================================================

# Install development tools
setup-tools:
    go install golang.org/x/vuln/cmd/govulncheck@latest
    go install github.com/securego/gosec/v2/cmd/gosec@latest
    go install honnef.co/go/tools/cmd/staticcheck@latest
    go install github.com/gitleaks/gitleaks/v8@latest

# Install git hooks (runs scripts/pre-push-hook.sh — skips tests when only test-report artifacts changed)
setup-hooks:
    @cp scripts/pre-push-hook.sh .git/hooks/pre-push
    @chmod +x .git/hooks/pre-push
    @echo "Installed .git/hooks/pre-push -> scripts/pre-push-hook.sh"

# Full development setup
setup: setup-tools setup-hooks
