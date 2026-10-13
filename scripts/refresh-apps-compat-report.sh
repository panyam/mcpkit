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

if ! command -v pnpm >/dev/null 2>&1; then
    echo "refresh-apps-compat-report: pnpm not found; tools/compat-reports needs it." >&2
    echo "  Install with 'npm i -g pnpm' or 'corepack enable pnpm'." >&2
    exit 1
fi

mkdir -p "$(dirname "$OUT")"

cd "$REPO_ROOT/tools/compat-reports"
# Was `npx --yes tsx@^4.0.0`, which resolved a floating tsx at run time — a
# reproducibility hole in a script whose output is byte-compared by a staleness
# gate. pnpm runs the version the committed lockfile pins.
pnpm install --silent || exit 1
exec pnpm exec tsx src/apps.ts \
    --umbrella "$UMBRELLA_NUMBER" \
    --repo "$UMBRELLA_REPO" \
    --out "$OUT"
