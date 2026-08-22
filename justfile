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
SUB_MODS_TO_TAG := "ext/auth ext/otel ext/ui ext/tasks ext/skills stores/redis experimental/ext/agents experimental/ext/agents/clients/go experimental/ext/events experimental/ext/events/stores/memory experimental/ext/events/stores/gorm experimental/ext/events/stores/redis experimental/ext/events/clients/go cmd/testclient cmd/common cmd/mcpskills examples/mcpskills-walkthrough tests/e2e tests/keycloak"

REPORT_DIR := "tests/reports"

# Discovers every sub-module go.mod (root excluded). Kept as a command string
# (not a backtick expression) so `find` only runs when a consuming recipe
# executes, not on every just invocation. Consumers: tidy-all, bump-root.
SUB_MODS_FIND := "find . -name go.mod -not -path '*/node_modules/*' -not -path '*/.claude/*' -not -path './go.mod' | sed 's|^\\./||;s|/go.mod$||' | sort"

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

# Run full client conformance suite — core + auth + extensions (delegates to conformance/justfile)
testconf-client:
    just -f conformance/justfile testconf-client

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

# CI gate — fail if a third-party dep is pinned at 2+ versions across published modules
check-dep-consistency:
    python3 scripts/check_dep_consistency.py

# Accept the current dependency divergence as the new baseline
update-dep-baseline:
    python3 scripts/check_dep_consistency.py --update-baseline

# Repo-wide dependency sweep (usage: just dep-sweep [patch|minor]); see scripts/dep-sweep.sh
dep-sweep MODE="minor":
    bash scripts/dep-sweep.sh {{MODE}}

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

