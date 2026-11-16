#!/usr/bin/env bash
# The exercise for mission #1710: a newcomer runs an mcpkit example in the
# browser with nothing to install.
#
#   make exercise-web-examples
#
# For each wasm-ready example: its scenario test natively, the same test under
# GOOS=js GOARCH=wasm with node, then the browser page build. Before that, the
# server package's RunWorker test under wasm. After them, every page loads in
# Chromium and calls a tool, the way the docs site serves it under /try/.
# Exits 1 if any step failed.
#
# To look at a page by hand: make -C examples/getting-started serve-web
set -u
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LOG=$(mktemp -d -t exercise-web-examples.XXXXXX)
EXAMPLES=(getting-started)
fail=0

row() { # name, log, command...
  local name="$1" log="$2"; shift 2
  if "$@" >"$log" 2>&1; then printf '  %-44s PASS\n' "$name"
  else printf '  %-44s FAIL (%s)\n' "$name" "$log"; fail=1; fi
}

echo "=== exercise-web-examples @ $(git -C "$ROOT" rev-parse --short HEAD) ==="
row "server RunWorker (wasm)" "$LOG/server.log" make -C "$ROOT" test-wasm
for ex in "${EXAMPLES[@]}"; do
  dir="$ROOT/examples/$ex"
  row "$ex scenario (native)" "$LOG/$ex-native.log" bash -c "cd '$dir' && go test ./... -count=1 -timeout 60s"
  row "$ex scenario (wasm)" "$LOG/$ex-wasm.log" "$ROOT/scripts/go-test-wasm.sh" "$dir" -timeout 120s ./server
done
row "build every browser page" "$LOG/collect.log" "$ROOT/scripts/collect_web_examples.sh" "$LOG/try"
grep -h '^built ' "$LOG/collect.log" | sed -E 's|^built .*/examples/(.+)/web/dist|    \1|' || true
row "every page in Chromium" "$LOG/pages.log" make -C "$ROOT" test-web-examples
grep -hE '^ +[0-9]+ (passed|failed)' "$LOG/pages.log" | sed 's/^ */    /' || true
exit $fail
