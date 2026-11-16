#!/usr/bin/env bash
# Builds every browser-ready example and copies its page into the docs site
# output, at docs/site/dist/docs/try/<example>/. The site serves it at
# https://panyam.github.io/mcpkit/try/<example>/ (#1712).
#
# An example counts as browser-ready when it has web/playground.json: the
# landing page's title and summary, a docs link, the smoke call
# tests/web-examples makes, and optionally the package to build and the wires
# to offer (examples/CONVENTIONS.md § Browser playground). Every one is served
# by the shared playground page in examples/common/web. The landing page at
# <DEST>/index.html comes from the manifests.
# Runs as a tail step of docs/site/scripts/build.sh.
#
#   scripts/collect_web_examples.sh [DEST]
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEST="${1:-$ROOT/docs/site/dist/docs/try}"
for main in "$ROOT"/examples/*/web/main.go; do
  [ -e "$main" ] || continue
  [ -f "$(dirname "$main")/playground.json" ] || {
    echo "collect-web-examples: $(dirname "$main") has main.go but no playground.json" >&2; exit 1; }
done
n=0
for manifest in "$ROOT"/examples/*/web/playground.json; do
  [ -e "$manifest" ] || continue
  dir="$(dirname "$(dirname "$manifest")")"
  name="$(basename "$dir")"
  "$ROOT/scripts/build-example-wasm.sh" "$dir"
  rm -rf "${DEST:?}/$name"
  mkdir -p "$DEST/$name"
  cp -R "$dir/web/dist/." "$DEST/$name/"
  echo "collect-web-examples: $name -> $DEST/$name/"
  n=$((n + 1))
done
echo "collect-web-examples: $n example(s) collected"
python3 "$ROOT/scripts/web_examples_index.py" "$DEST"
