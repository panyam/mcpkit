#!/usr/bin/env bash
# Builds every browser-ready example the way the docs site does, then loads
# each page in Chromium and calls its first tool (#1712).
#
#   make test-web-examples
#
# Needs node, pnpm and Go. The pages go to tests/web-examples/.site/try/,
# the same layout as https://panyam.github.io/mcpkit/try/.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIR="$ROOT/tests/web-examples"
rm -rf "$DIR/.site"
"$ROOT/scripts/collect_web_examples.sh" "$DIR/.site/try"
cd "$DIR"
pnpm install --frozen-lockfile --silent
pnpm exec playwright install chromium >/dev/null
pnpm exec playwright test "$@"
