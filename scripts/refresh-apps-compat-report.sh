#!/bin/bash
# Regenerates conformance/apps/COMPAT.md from the apps-compat umbrella
# tracking issue (panyam/mcpkit#533 by default). Thin shell wrapper —
# the actual parser + renderer lives in tools/compat-reports/src/apps.ts
# so it shares the umbrella-table parse + render helpers with future
# extension-compat reports (tasks-v2, mrtr, ...).
#
# Deterministic contract: re-running on an unchanged umbrella body
# produces a byte-identical file. The check-apps-compat-stale CI gate
# (mirroring check-conformance-stale) enforces refresh + git diff
# --exit-code on PRs that touch examples/apps/compat/**.
#
# Env overrides:
#   UMBRELLA_NUMBER  — GitHub issue number (default 533)
#   UMBRELLA_REPO    — owner/repo for the umbrella (default panyam/mcpkit)
#   REFRESH_OUT      — output path (default conformance/apps/COMPAT.md)
#   GH_TOKEN         — required for gh CLI; GH_PERSONAL_TOKEN takes
#                      precedence (EMU accounts can't read personal
#                      repos with the org token).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
UMBRELLA_NUMBER="${UMBRELLA_NUMBER:-533}"
UMBRELLA_REPO="${UMBRELLA_REPO:-panyam/mcpkit}"
OUT="${REFRESH_OUT:-$REPO_ROOT/conformance/apps/COMPAT.md}"

if ! command -v gh >/dev/null 2>&1; then
    echo "refresh-apps-compat-report: gh CLI required (https://cli.github.com)" >&2
    exit 1
fi

if ! command -v npx >/dev/null 2>&1; then
    echo "refresh-apps-compat-report: npx not found. Install Node.js 22+." >&2
    exit 1
fi

mkdir -p "$(dirname "$OUT")"

cd "$REPO_ROOT/tools/compat-reports"
exec npx --yes tsx@^4.0.0 src/apps.ts \
    --umbrella "$UMBRELLA_NUMBER" \
    --repo "$UMBRELLA_REPO" \
    --out "$OUT"
