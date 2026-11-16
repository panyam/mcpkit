#!/usr/bin/env bash
# Builds every browser-ready example and copies its page into the docs site
# output, at docs/site/dist/docs/try/<example>/. The site serves it at
# https://panyam.github.io/mcpkit/try/<example>/ (#1712).
#
# An example counts as browser-ready in one of two shapes, found by convention
# like collect_walkthroughs.py:
#   - web/main.go builds a bare server from the example's mcp/ package, and
#     the shared playground page serves it (examples/tasks-v2, #1714).
#   - web/worker.js plus a server/ package, with the example's own page
#     (examples/getting-started, #1712).
# Runs as a tail step of docs/site/scripts/build.sh.
#
#   scripts/collect_web_examples.sh [DEST]
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEST="${1:-$ROOT/docs/site/dist/docs/try}"
n=0
for web in "$ROOT"/examples/*/web; do
  dir="$(dirname "$web")"
  name="$(basename "$dir")"
  [ "$name" = common ] && continue
  if [ -f "$web/main.go" ]; then
    pkg=./web
  elif [ -f "$web/worker.js" ] && [ -d "$dir/server" ]; then
    pkg=./server
  else
    echo "collect-web-examples: $name has web/ but neither web/main.go nor web/worker.js + server/, skipping" >&2
    continue
  fi
  "$ROOT/scripts/build-example-wasm.sh" "$dir" "$pkg"
  rm -rf "${DEST:?}/$name"
  mkdir -p "$DEST/$name"
  cp -R "$dir/web/dist/." "$DEST/$name/"
  echo "collect-web-examples: $name -> $DEST/$name/"
  n=$((n + 1))
done
echo "collect-web-examples: $n example(s) collected"
