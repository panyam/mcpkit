#!/usr/bin/env bash
# Builds an example's server for the browser: GOOS=js GOARCH=wasm, plus the
# example's web/ page and Go's wasm_exec.js, into <example>/web/dist/.
#
#   scripts/build-example-wasm.sh examples/getting-started ./server
#
# web/dist/ is gitignored. Never commit server.wasm (scripts/check-no-binaries.sh).
set -euo pipefail
dir="$(cd "$1" && pwd)"
pkg="${2:-./server}"
out="$dir/web/dist"
mkdir -p "$out"
(cd "$dir" && GOOS=js GOARCH=wasm go build -trimpath -ldflags='-s -w' -o "$out/server.wasm" "$pkg")
cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" "$out/"
cp "$dir/web/index.html" "$dir/web/worker.js" "$out/"
size=$(wc -c <"$out/server.wasm" | tr -d ' ')
gz=$(gzip -9 -c "$out/server.wasm" | wc -c | tr -d ' ')
echo "built $out (server.wasm $((size / 1024 / 1024)) MB, $((gz / 1024 / 1024)) MB gzipped)"
