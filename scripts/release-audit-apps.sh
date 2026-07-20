#!/usr/bin/env bash
# Release-time apps/compat audit umbrella — fully end-to-end: refresh the
# upstream ext-apps clone → docker-all (parity + visual gate) → regenerate the
# gallery → commit + push the gallery → deploy the docs site.
#
# Runner-agnostic: the root Makefile + justfile `release-audit-apps` recipes
# both call this with no arguments. Every step runs a script/python tool
# directly — no make/just invocation.
#
# set -eu mirrors the original just recipe: a failed clone / gallery / commit /
# push aborts. The docker-all step keeps its explicit `|| echo` so a fixture
# failure is captured in the gallery rather than aborting the run.
set -eu

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

echo "==> [1/5] Refreshing upstream ext-apps clone at /tmp/ext-apps..."
if [ -f /tmp/ext-apps/.git/HEAD ]; then
    (cd /tmp/ext-apps && git pull --quiet) && echo "  pulled"
elif [ -e /tmp/ext-apps ]; then
    rm -rf /tmp/ext-apps && git clone --quiet https://github.com/modelcontextprotocol/ext-apps.git /tmp/ext-apps && echo "  re-cloned (was corrupted: missing .git/HEAD)"
else
    git clone --quiet https://github.com/modelcontextprotocol/ext-apps.git /tmp/ext-apps && echo "  cloned"
fi
echo ""
echo "==> [2/5] Running docker-all (parity diff + Playwright visual gate across 21 fixtures)..."
uv run scripts/apps_playwright_test.py --docker --all || echo "  WARNING: docker-all failed. Continuing so drift is captured in the gallery for inspection."
echo ""
echo "==> [3/5] Regenerating visual gallery..."
uv run scripts/apps_visual_gallery.py
echo "Gallery refreshed. Commit docs/site/content/conformance/apps/visual-gallery/ + docs/site/static/conformance/apps/visual-gallery/."
echo ""
echo "==> [4/5] Committing + pushing regenerated gallery artifacts..."
if git status --porcelain docs/site/content/conformance/apps/visual-gallery/ docs/site/static/conformance/apps/visual-gallery/ 2>/dev/null | grep -q .; then
    git add docs/site/content/conformance/apps/visual-gallery/ docs/site/static/conformance/apps/visual-gallery/
    git commit -m "refresh: visual gallery for release"
    git push
    echo "  committed + pushed"
else
    echo "  no gallery changes; nothing to commit"
fi
echo ""
echo "==> [5/5] Deploying docs site to gh-pages..."
bash docs/site/scripts/gh-pages.sh
echo ""
echo "==> Release audit complete."
echo "    Gallery: https://panyam.github.io/mcpkit/conformance/apps/visual-gallery/"
echo "    (gh-pages CDN may take 1-5 min to flush)"
