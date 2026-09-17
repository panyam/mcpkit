#!/usr/bin/env bash
# Grades the mcpkit CLIENT against the external stateless-draft checker.
# Live network. Output: conformance/EXTERNAL_CHECKER.md.
set -euo pipefail

REPO_ROOT="${REPO_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
cd "$REPO_ROOT"

if [ -n "${EXTERNAL_CHECKER_URL:-}" ]; then
    exec go run ./cmd/external-checker -url "$EXTERNAL_CHECKER_URL"
fi
exec go run ./cmd/external-checker
