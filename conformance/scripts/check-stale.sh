#!/usr/bin/env bash
# CI gate — CONFORMANCE.md and its badge JSON are generated, so a diff against
# HEAD means the committed copies are stale (issue 498). Compares the working
# tree to HEAD rather than the index, so the gate is robust to staging order.
set -uo pipefail

REPO_ROOT="${REPO_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
RUNNER="${RUNNER:-make}"
FILES="CONFORMANCE.md docs/site/static/conformance/badge.json"

cd "$REPO_ROOT"

if git diff --exit-code HEAD -- $FILES >/dev/null 2>&1; then
    echo "CONFORMANCE.md is up to date."
    exit 0
fi

echo "CONFORMANCE.md (or its badge JSON) is stale relative to current testserver + upstream conformance."
echo "Run '$RUNNER refresh-conformance' locally and commit the regenerated files."
echo ""
# Both files, as the Makefile copy did. The justfile copy printed only
# CONFORMANCE.md, so a badge-only drift showed an empty diff under the message.
git diff HEAD -- $FILES | head -60
exit 1
