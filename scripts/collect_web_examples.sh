#!/usr/bin/env bash
# Builds every browser-ready example and copies its page into the docs site
# output, at docs/site/dist/docs/try/<example>/. The site serves it at
# https://panyam.github.io/mcpkit/try/<example>/ (#1712).
#
# An example counts as browser-ready when it has web/worker.js and a server/
# package (examples/getting-started is the reference). Discovery is by that
# convention, like collect_walkthroughs.py. Runs as a tail step of
# docs/site/scripts/build.sh.
#
#   scripts/collect_web_examples.sh [DEST]
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEST="${1:-$ROOT/docs/site/dist/docs/try}"
n=0
for worker in "$ROOT"/examples/*/web/worker.js; do
  [ -e "$worker" ] || continue
  dir="$(dirname "$(dirname "$worker")")"
  name="$(basename "$dir")"
  [ -d "$dir/server" ] || { echo "collect-web-examples: $name has web/ but no server/, skipping" >&2; continue; }
  "$ROOT/scripts/build-example-wasm.sh" "$dir" ./server
  rm -rf "${DEST:?}/$name"
  mkdir -p "$DEST/$name"
  cp -R "$dir/web/dist/." "$DEST/$name/"
  echo "collect-web-examples: $name -> $DEST/$name/"
  n=$((n + 1))
done
echo "collect-web-examples: $n example(s) collected"
